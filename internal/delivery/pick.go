// Package delivery는 매일 발송할 콘텐츠를 선택하는 로직을 담는다.
//
// 핵심 원칙: 같은 repo를 구독한 사용자는 같은 날 같은 콘텐츠를 받는다 (뉴스 모델).
//   - 각 repo의 "오늘의 콘텐츠"는 rotation_count, send_order 오름차순 1개로 결정됨
//   - 사용자는 구독한 repo 중 가중치 확률로 1개 repo를 뽑고, 그 repo의 오늘 콘텐츠를 받음
//   - 가중 추첨은 (subscriber, day) 시드로 결정론적이라 재시도 시 동일 결과를 보장
package delivery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"
)

type Content struct {
	RepoSlug      string
	ContentID     string
	Title         string
	Preview       string
	BodyPath      string
	DiscussionURL sql.NullString
	RotationCount int
}

// PickForSubscriber chooses which today's content to deliver to subscriberID.
//
// Rules:
//   - paused / unsubscribed subscribers → returns (nil, nil)
//   - subscriber with no active subscriptions → (nil, nil)
//   - subscriber whose subscribed repos have no eligible content → (nil, nil)
//   - otherwise picks one repo proportionally to subscription weights, then
//     returns that repo's today's content
//
// The pick is deterministic for a given (subscriberID, today): retries on the
// same day return the same content. This makes retries idempotent.
func PickForSubscriber(ctx context.Context, db *sql.DB, subscriberID int64, today time.Time) (*Content, error) {
	if active, err := isActiveSubscriber(ctx, db, subscriberID); err != nil {
		return nil, err
	} else if !active {
		return nil, nil
	}

	subs, err := loadSubscriptions(ctx, db, subscriberID)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, nil
	}

	type candidate struct {
		weight  int
		content *Content
	}
	candidates := make([]candidate, 0, len(subs))
	for _, s := range subs {
		c, err := TodayContentForRepo(ctx, db, s.RepoSlug)
		if err != nil {
			return nil, err
		}
		if c == nil {
			continue
		}
		candidates = append(candidates, candidate{weight: s.Weight, content: c})
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	totalWeight := 0
	for _, c := range candidates {
		totalWeight += c.weight
	}

	seed := dailyPickSeed(subscriberID, today)
	target := seed % uint64(totalWeight)
	acc := uint64(0)
	for _, c := range candidates {
		acc += uint64(c.weight)
		if target < acc {
			return c.content, nil
		}
	}
	// unreachable when totalWeight > 0
	return candidates[len(candidates)-1].content, nil
}

type subscription struct {
	RepoSlug string
	Weight   int
}

func isActiveSubscriber(ctx context.Context, db *sql.DB, id int64) (bool, error) {
	var paused, unsubscribed sql.NullTime
	var confirmed sql.NullTime
	err := db.QueryRowContext(ctx,
		`SELECT confirmed_at, paused_at, unsubscribed_at FROM subscribers WHERE id = ?`,
		id,
	).Scan(&confirmed, &paused, &unsubscribed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load subscriber %d: %w", id, err)
	}
	if !confirmed.Valid {
		return false, nil
	}
	if paused.Valid || unsubscribed.Valid {
		return false, nil
	}
	return true, nil
}

func loadSubscriptions(ctx context.Context, db *sql.DB, id int64) ([]subscription, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.repo_slug, s.weight
		  FROM subscriptions s
		  JOIN repos r ON r.slug = s.repo_slug
		 WHERE s.subscriber_id = ? AND r.active = 1
		 ORDER BY s.repo_slug`, id)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}
	defer rows.Close()
	var out []subscription
	for rows.Next() {
		var s subscription
		if err := rows.Scan(&s.RepoSlug, &s.Weight); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// dailyPickSeed returns a deterministic seed for (subscriberID, today).
// SHA-256 keeps the distribution uniform across small input ranges.
func dailyPickSeed(subscriberID int64, today time.Time) uint64 {
	key := strconv.FormatInt(subscriberID, 10) + "|" + today.Format("2006-01-02")
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}

// TodayContentForRepo returns the single content that any subscriber of repoSlug
// will receive today. Returns (nil, nil) if the repo has no eligible content.
func TodayContentForRepo(ctx context.Context, db *sql.DB, repoSlug string) (*Content, error) {
	var c Content
	err := db.QueryRowContext(ctx, `
		SELECT repo_slug, content_id, title, preview, body_path,
		       discussion_url, rotation_count
		  FROM contents
		 WHERE repo_slug = ? AND deleted_at IS NULL
		 ORDER BY rotation_count ASC, content_id ASC
		 LIMIT 1`, repoSlug,
	).Scan(
		&c.RepoSlug, &c.ContentID, &c.Title, &c.Preview, &c.BodyPath,
		&c.DiscussionURL, &c.RotationCount,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query today content for %s: %w", repoSlug, err)
	}
	return &c, nil
}
