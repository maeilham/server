package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/store"
)

type NotifyStats struct {
	NewQuestions int // 오늘 알림 대상인 새 질문 수
	Subscribers  int
	Sent         int
	Skipped      int // 이미 오늘 메일을 받았거나 구독한 repo에 새 질문이 없는 사용자
	Errors       int
	DryRun       bool
}

type NotifyOptions struct {
	Day       time.Time      // 보통 time.Now(). 테스트/디버깅용으로 주입 가능. 어느 시간대 표기든 Loc 기준의 하루로 바뀐다
	Loc       *time.Location // 서비스의 하루를 나누는 시간대(MAEILHAM_TZ). 필수 — 빠뜨리면 하루가 UTC로 계산되는 버그가 되살아난다
	DryRun    bool           // true면 실제 발송 없이 결정만 로그
	BaseURL   string         // 웹 프론트 URL
	Secret    string         // HMAC 서명 키 (unsubscribe 토큰 생성)
	GitHubApp *gh.App

	SubRepo       store.SubscriberRepository
	RepoStore     store.RepoRepository
	ContentStore  store.ContentRepository
	LogStore      store.DeliveryLogRepository
	QuestionStore store.DailyQuestionRepository
}

// NotifyNewQuestions는 오늘 처음 나오는 새 글(복습이 아닌 오늘의 질문)을 구독자에게 알린다.
// 오늘의 질문이 아직 없으면 여기서 정하므로 웹 방문과 상관없이 같은 결과다. 복습일에는 아무것도 보내지 않는다.
// 구독자마다 하루 한 통이고(delivery_log), 모든 발송이 끝난 질문만 알림 처리 완료로 표시해서 실패하면 다음에 남은 사람부터 이어 보낸다.
func NotifyNewQuestions(
	ctx context.Context,
	logger *slog.Logger,
	mailer mail.Mailer,
	opts NotifyOptions,
) (*NotifyStats, error) {
	stats := &NotifyStats{DryRun: opts.DryRun}

	if opts.Loc == nil {
		return stats, errors.New("NotifyOptions.Loc is required (service time zone, MAEILHAM_TZ)")
	}
	if opts.QuestionStore == nil {
		return stats, errors.New("NotifyOptions.QuestionStore is required (daily question store)")
	}
	dayStart, dayEnd := ServiceDay(opts.Day, opts.Loc)
	day := ServiceDate(opts.Day, opts.Loc)

	// 1. 오늘 알릴 새 질문 모으기. 복습이거나 이미 처리한 질문은 제외하고, 질문이 아직 없으면 여기서 정함
	repos, err := opts.RepoStore.ListActive(ctx)
	if err != nil {
		return stats, fmt.Errorf("load repos: %w", err)
	}
	repoMap := buildRepoMap(repos)

	type pending struct {
		question *store.DailyQuestion
		content  *store.Content
	}
	var news []pending
	for _, r := range repos {
		q, err := opts.QuestionStore.Ensure(ctx, day, r.Slug)
		if err != nil {
			logger.Error("ensure daily question", "repo", r.Slug, "err", err)
			stats.Errors++
			continue
		}
		if q == nil || q.Review || q.Notified {
			continue
		}
		c, err := opts.ContentStore.GetByRepoAndID(ctx, q.RepoSlug, q.ContentID)
		if err != nil || c == nil {
			logger.Error("load content of daily question", "repo", r.Slug, "content", q.ContentID, "err", err)
			stats.Errors++
			continue
		}
		news = append(news, pending{q, c})
	}
	stats.NewQuestions = len(news)
	if len(news) == 0 {
		return stats, nil
	}

	// 2. 새 질문의 Discussion 만들기. 실패해도 알림은 계속함
	if !opts.DryRun && opts.GitHubApp != nil {
		for _, n := range news {
			if n.content.DiscussionURL != "" {
				continue
			}
			if _, err := createDiscussion(ctx, opts.GitHubApp, opts.ContentStore, n.content, repoMap[n.content.RepoSlug]); err != nil {
				logger.Warn("discussion create failed (non-fatal)", "content", n.content.ContentID, "err", err)
			}
		}
	}

	// 3. 구독자마다 한 통. 이미 받았으면 제외하고, 구독한 repo의 새 질문만 묶음
	subs, err := opts.SubRepo.ListActive(ctx)
	if err != nil {
		return stats, fmt.Errorf("load subscribers: %w", err)
	}
	stats.Subscribers = len(subs)

	for _, sub := range subs {
		l := logger.With("subscriber_id", sub.ID, "email", sub.Email)

		already, err := opts.LogStore.AlreadySent(ctx, sub.ID, dayStart, dayEnd)
		if err != nil {
			l.Error("check already-delivered", "err", err)
			stats.Errors++
			continue
		}
		if already {
			stats.Skipped++
			continue
		}

		subscribed, err := opts.SubRepo.LoadSubscriptions(ctx, sub.ID)
		if err != nil {
			l.Error("load subscriptions", "err", err)
			stats.Errors++
			continue
		}
		slugs := make(map[string]bool, len(subscribed))
		for _, s := range subscribed {
			slugs[s.RepoSlug] = true
		}

		var items []mail.NewQuestion
		var mine []*store.Content
		for _, n := range news {
			if !slugs[n.content.RepoSlug] {
				continue
			}
			repoName := n.content.RepoSlug
			if r := repoMap[n.content.RepoSlug]; r != nil {
				repoName = r.DisplayName
			}
			items = append(items, mail.NewQuestion{RepoName: repoName, Title: n.content.Title, Preview: n.content.Preview})
			mine = append(mine, n.content)
		}
		if len(items) == 0 {
			stats.Skipped++
			continue
		}

		subject, text, htmlBody := mail.RenderNewQuestions(mail.NewQuestionMailData{
			Questions:      items,
			WebURL:         opts.BaseURL,
			UnsubscribeURL: buildUnsubscribeURL(opts.BaseURL, opts.Secret, sub.Email),
		})

		// 4. 발송과 기록. dry-run이면 로그만 남김
		if opts.DryRun {
			l.Info("dry-run notify", "questions", len(items), "subject", subject)
			stats.Sent++
			continue
		}

		if err := mailer.Send(ctx, mail.Message{To: sub.Email, Subject: subject, TextBody: text, HTMLBody: htmlBody}); err != nil {
			l.Error("send", "err", err)
			stats.Errors++
			continue
		}
		for _, c := range mine {
			if err := opts.LogStore.Record(ctx, sub.ID, c.RepoSlug, c.ContentID, "email", opts.Day); err != nil {
				l.Error("record delivery", "err", err)
				stats.Errors++
			}
		}
		stats.Sent++
	}

	// 5. 실패 없이 끝났을 때만 완료 표시. 실패했으면 다음 실행이 남은 사람부터 이어 보냄
	if opts.DryRun || stats.Errors > 0 {
		return stats, nil
	}
	for _, n := range news {
		if err := opts.QuestionStore.MarkNotified(ctx, day, n.question.RepoSlug); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func buildRepoMap(repos []*store.Repo) map[string]*store.Repo {
	m := make(map[string]*store.Repo, len(repos))
	for _, r := range repos {
		m[r.Slug] = r
	}
	return m
}

func createDiscussion(
	ctx context.Context,
	app *gh.App,
	contentStore store.ContentRepository,
	c *store.Content,
	repo *store.Repo,
) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("repo not found for %s", c.RepoSlug)
	}
	owner, repoName, err := parseOwnerRepo(repo.GitHubURL)
	if err != nil {
		return "", err
	}

	repoID, categories, err := app.RepoMeta(ctx, owner, repoName)
	if err != nil {
		return "", fmt.Errorf("repo meta: %w", err)
	}

	categoryID := repo.DiscussionCategoryID
	if categoryID == "" {
		if id, ok := categories["General"]; ok {
			categoryID = id
		} else {
			for _, id := range categories {
				categoryID = id
				break
			}
		}
	}
	if categoryID == "" {
		return "", fmt.Errorf("no discussion category found")
	}

	title := fmt.Sprintf("[매일함] %s", c.Title)
	body := fmt.Sprintf("## %s\n\n%s\n\n---\n*매일함에서 자동으로 생성된 Discussion입니다. 자유롭게 답변을 달아주세요!*",
		c.Title, c.Preview)

	url, nodeID, err := app.CreateDiscussion(ctx, owner, repoName, repoID, categoryID, title, body)
	if err != nil {
		return "", err
	}

	_ = contentStore.SaveDiscussionURL(ctx, c.RepoSlug, c.ContentID, url, nodeID)
	return url, nil
}

func parseOwnerRepo(githubURL string) (owner, repo string, err error) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(githubURL, "https://github.com/"), "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid github url: %s", githubURL)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

func buildUnsubscribeURL(baseURL, secret, email string) string {
	if baseURL == "" || secret == "" {
		return ""
	}
	exp := time.Now().Add(365 * 24 * time.Hour).Unix()
	msg := fmt.Sprintf("%s:%d", email, exp)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	payload := base64.RawURLEncoding.EncodeToString([]byte(msg))
	token := payload + "." + sig
	return fmt.Sprintf("%s/?action=unsubscribe&token=%s", strings.TrimSuffix(baseURL, "/"), token)
}
