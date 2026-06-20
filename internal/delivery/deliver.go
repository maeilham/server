package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/closeutil"
)

type DailySendStats struct {
	Subscribers int // 활성 구독자 수
	Picked      int // 콘텐츠가 매칭된 사용자 수
	Sent        int // 실제 메일 발송 성공
	Skipped     int // 이미 오늘 받은 사용자 (멱등 스킵)
	NoContent   int // 픽이 nil인 사용자
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
	GitHubApp *gh.App   // nil이면 Discussion 생성 스킵
}

// DailySend runs one daily-send batch. Walks active subscribers, picks today's
// content for each, renders mail, sends, and records delivery_log. After all
// sends are attempted, advances rotation_count for every content that received
// at least one delivery.
func DailySend(
	ctx context.Context,
	logger *slog.Logger,
	db *sql.DB,
	mailer mail.Mailer,
	opts DailySendOptions,
) (*DailySendStats, error) {
	stats := &DailySendStats{DryRun: opts.DryRun}

	subs, err := loadActiveSubscribers(ctx, db)
	if err != nil {
		return stats, fmt.Errorf("load subscribers: %w", err)
	}
	stats.Subscribers = len(subs)

	repoInfo, err := loadRepoInfo(ctx, db)
	if err != nil {
		return stats, fmt.Errorf("load repo info: %w", err)
	}

	for _, sub := range subs {
		l := logger.With("subscriber_id", sub.ID, "email", sub.Email)

		already, err := AlreadyDeliveredToday(ctx, db, sub.ID, opts.Day)
		if err != nil {
			l.Error("check already-delivered", "err", err)
			stats.Errors++
			continue
		}
		if already {
			stats.Skipped++
			continue
		}

		content, err := PickForSubscriber(ctx, db, sub.ID, opts.Day)
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

		info := repoInfo[content.RepoSlug]

		// Discussion이 아직 없으면 생성
		if opts.GitHubApp != nil && (!content.DiscussionURL.Valid || content.DiscussionURL.String == "") {
			if discURL, err := createDiscussion(ctx, opts.GitHubApp, db, content, info); err != nil {
				logger.Warn("discussion create failed (non-fatal)", "content", content.ContentID, "err", err)
			} else {
				content.DiscussionURL = sql.NullString{String: discURL, Valid: true}
			}
		}

		data := mail.DailyMailData{
			RepoSlug:       content.RepoSlug,
			RepoName:       info.DisplayName,
			Title:          content.Title,
			Preview:        content.Preview,
			GitHubURL:      buildGitHubURL(info.GitHubURL, content.BodyPath),
			DiscussionURL:  discussionURLOrFallback(content, info.GitHubURL),
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
		if err := RecordDelivery(ctx, db, sub.ID, content, "email", opts.Day); err != nil {
			l.Error("record delivery", "err", err)
			stats.Errors++
			continue
		}
		stats.Sent++
	}

	if !opts.DryRun {
		advanced, err := AdvanceRotation(ctx, db, opts.Day)
		if err != nil {
			logger.Error("advance rotation", "err", err)
			stats.Errors++
		}
		stats.Advanced = advanced
	}

	return stats, nil
}

type activeSubscriber struct {
	ID    int64
	Email string
}

func loadActiveSubscribers(ctx context.Context, db *sql.DB) ([]activeSubscriber, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, email
		  FROM subscribers
		 WHERE confirmed_at IS NOT NULL
		   AND paused_at IS NULL
		   AND unsubscribed_at IS NULL
		 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer closeutil.Discard(rows)
	var out []activeSubscriber
	for rows.Next() {
		var s activeSubscriber
		if err := rows.Scan(&s.ID, &s.Email); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type repoMeta struct {
	GitHubURL   string
	DisplayName string
}

func loadRepoInfo(ctx context.Context, db *sql.DB) (map[string]repoMeta, error) {
	rows, err := db.QueryContext(ctx, `SELECT slug, github_url, display_name FROM repos`)
	if err != nil {
		return nil, err
	}
	defer closeutil.Discard(rows)
	out := make(map[string]repoMeta)
	for rows.Next() {
		var slug string
		var m repoMeta
		if err := rows.Scan(&slug, &m.GitHubURL, &m.DisplayName); err != nil {
			return nil, err
		}
		out[slug] = m
	}
	return out, rows.Err()
}

func createDiscussion(ctx context.Context, app *gh.App, db *sql.DB, c *Content, info repoMeta) (string, error) {
	owner, repo, err := parseOwnerRepo(info.GitHubURL)
	if err != nil {
		return "", err
	}

	repoID, categories, err := app.RepoMeta(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("repo meta: %w", err)
	}

	// DB에서 카테고리 ID 조회
	var categoryID string
	row := db.QueryRowContext(ctx,
		`SELECT COALESCE(discussion_category_id, '') FROM repos WHERE slug = ?`, c.RepoSlug)
	if err := row.Scan(&categoryID); err != nil || categoryID == "" {
		// fallback: "General" 또는 첫 번째 카테고리
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

	url, nodeID, err := app.CreateDiscussion(ctx, repoID, categoryID, title, body)
	if err != nil {
		return "", err
	}

	_, _ = db.ExecContext(ctx,
		`UPDATE contents SET discussion_url = ?, discussion_node_id = ? WHERE repo_slug = ? AND content_id = ?`,
		url, nodeID, c.RepoSlug, c.ContentID)

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

func discussionURLOrFallback(c *Content, repoGitHubURL string) string {
	if c.DiscussionURL.Valid && c.DiscussionURL.String != "" {
		return c.DiscussionURL.String
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

