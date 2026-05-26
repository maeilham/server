package delivery

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/maeilham/server/internal/mail"
)

type DailySendStats struct {
	Subscribers   int // 활성 구독자 수
	Picked        int // 콘텐츠가 매칭된 사용자 수
	Sent          int // 실제 메일 발송 성공
	Skipped       int // 이미 오늘 받은 사용자 (멱등 스킵)
	NoContent     int // 픽이 nil인 사용자
	Errors        int
	Advanced      int64 // rotation_count 갱신된 콘텐츠 수
	DryRun        bool
}

type DailySendOptions struct {
	Day        time.Time // 보통 time.Now(). 테스트/디버깅용으로 주입 가능
	DryRun     bool      // true면 실제 발송 없이 결정만 로그
	BaseURL    string    // 매일함 사이트 URL (unsubscribe 링크 만들 때)
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
		data := mail.DailyMailData{
			RepoSlug:       content.RepoSlug,
			RepoName:       info.DisplayName,
			Title:          content.Title,
			Preview:        content.Preview,
			GitHubURL:      buildGitHubURL(info.GitHubURL, content.BodyPath),
			DiscussionURL:  discussionURLOrFallback(content, info.GitHubURL),
			UnsubscribeURL: buildUnsubscribeURL(opts.BaseURL, sub.ID),
		}
		subject, text := mail.RenderDaily(data)

		if opts.DryRun {
			l.Info("dry-run pick",
				"repo", content.RepoSlug, "content", content.ContentID, "subject", subject)
			stats.Sent++
			continue
		}

		msg := mail.Message{To: sub.Email, Subject: subject, TextBody: text}
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
	defer rows.Close()
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
	defer rows.Close()
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

func buildUnsubscribeURL(baseURL string, subscriberID int64) string {
	if baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/unsubscribe?sid=%d", strings.TrimSuffix(baseURL, "/"), subscriberID)
}
