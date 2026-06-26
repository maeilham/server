package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/store"
)

// EnsureDiscussion returns the Discussion URL for contentID, creating one if it doesn't exist yet.
func EnsureDiscussion(
	ctx context.Context,
	app *gh.App,
	contentStore store.ContentRepository,
	repoStore store.RepoRepository,
	contentID string,
) (string, error) {
	if app == nil {
		return "", nil
	}
	c, err := contentStore.GetByID(ctx, contentID)
	if err != nil || c == nil {
		return "", err
	}
	if c.DiscussionURL != "" {
		return c.DiscussionURL, nil
	}
	repos, err := repoStore.ListAll(ctx)
	if err != nil {
		return "", err
	}
	repoMap := buildRepoMap(repos)
	return createDiscussion(ctx, app, contentStore, c, repoMap[c.RepoSlug])
}

type DailySendStats struct {
	Subscribers int
	Picked      int
	Sent        int
	Skipped     int // 이미 오늘 받은 사용자 (멱등 스킵)
	NoContent   int
	Errors      int
	Advanced    int64 // rotation_count 갱신된 콘텐츠 수
	DryRun      bool
}

type DailySendOptions struct {
	Day       time.Time // 보통 time.Now(). 테스트/디버깅용으로 주입 가능
	DryRun    bool      // true면 실제 발송 없이 결정만 로그
	BaseURL   string    // 웹 프론트 URL (리다이렉트용)
	APIURL    string    // API 서버 URL (메일 링크용)
	Secret    string    // HMAC 서명 키 (unsubscribe 토큰 생성)
	GitHubApp *gh.App

	SubRepo      store.SubscriberRepository
	RepoStore    store.RepoRepository
	ContentStore store.ContentRepository
	LogStore     store.DeliveryLogRepository
}

// DailySend runs one daily-send batch. Walks active subscribers, picks today's
// content for each, renders mail, sends, and records delivery_log. After all
// sends are attempted, advances rotation_count for every content that received
// at least one delivery.
func DailySend(
	ctx context.Context,
	logger *slog.Logger,
	mailer mail.Mailer,
	opts DailySendOptions,
) (*DailySendStats, error) {
	stats := &DailySendStats{DryRun: opts.DryRun}

	subs, err := opts.SubRepo.ListActive(ctx)
	if err != nil {
		return stats, fmt.Errorf("load subscribers: %w", err)
	}
	stats.Subscribers = len(subs)

	repos, err := opts.RepoStore.ListAll(ctx)
	if err != nil {
		return stats, fmt.Errorf("load repo info: %w", err)
	}
	repoMap := buildRepoMap(repos)

	for _, sub := range subs {
		l := logger.With("subscriber_id", sub.ID, "email", sub.Email)

		already, err := opts.LogStore.AlreadySentToday(ctx, sub.ID, opts.Day)
		if err != nil {
			l.Error("check already-delivered", "err", err)
			stats.Errors++
			continue
		}
		if already {
			stats.Skipped++
			continue
		}

		content, err := PickForSubscriber(ctx, opts.SubRepo, opts.ContentStore, sub.ID, opts.Day)
		if err != nil {
			l.Error("pick", "err", err)
			stats.Errors++
			continue
		}
		if content == nil {
			stats.NoContent++
			continue
		}
		stats.Picked++

		repo := repoMap[content.RepoSlug]

		if opts.GitHubApp != nil && content.DiscussionURL == "" {
			if discURL, err := createDiscussion(ctx, opts.GitHubApp, opts.ContentStore, content, repo); err != nil {
				logger.Warn("discussion create failed (non-fatal)", "content", content.ContentID, "err", err)
			} else {
				content.DiscussionURL = discURL
			}
		}

		var repoName, repoGitHubURL string
		if repo != nil {
			repoName = repo.DisplayName
			repoGitHubURL = repo.GitHubURL
		}

		data := mail.DailyMailData{
			RepoSlug:       content.RepoSlug,
			RepoName:       repoName,
			Title:          content.Title,
			Preview:        content.Preview,
			GitHubURL:      buildGitHubURL(repoGitHubURL, content.BodyPath),
			DiscussionURL:  discussionURLOrFallback(content.DiscussionURL, repoGitHubURL),
			UnsubscribeURL: buildUnsubscribeURL(opts.APIURL, opts.Secret, sub.Email),
		}
		subject, text, htmlBody := mail.RenderDaily(data)

		if opts.DryRun {
			l.Info("dry-run pick",
				"repo", content.RepoSlug, "content", content.ContentID, "subject", subject)
			stats.Sent++
			continue
		}

		msg := mail.Message{To: sub.Email, Subject: subject, TextBody: text, HTMLBody: htmlBody}
		if err := mailer.Send(ctx, msg); err != nil {
			l.Error("send", "err", err)
			stats.Errors++
			continue
		}
		if err := opts.LogStore.Record(ctx, sub.ID, content.RepoSlug, content.ContentID, "email", opts.Day); err != nil {
			l.Error("record delivery", "err", err)
			stats.Errors++
			continue
		}
		stats.Sent++
	}

	if !opts.DryRun {
		advanced, err := opts.ContentStore.AdvanceRotation(ctx, opts.Day)
		if err != nil {
			logger.Error("advance rotation", "err", err)
			stats.Errors++
		}
		stats.Advanced = advanced
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

func buildGitHubURL(repoGitHubURL, bodyPath string) string {
	base := strings.TrimSuffix(repoGitHubURL, "/")
	base = strings.TrimSuffix(base, ".git")
	return fmt.Sprintf("%s/blob/main/%s", base, bodyPath)
}

func discussionURLOrFallback(discussionURL, repoGitHubURL string) string {
	if discussionURL != "" {
		return discussionURL
	}
	base := strings.TrimSuffix(repoGitHubURL, "/")
	base = strings.TrimSuffix(base, ".git")
	return base + "/discussions"
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
