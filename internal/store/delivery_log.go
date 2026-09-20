package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type DeliveryLogRepository interface {
	// LastDeliveredContentID returns the content most recently delivered from repoSlug during [from, to).
	// 구간은 시각 범위로 비교한다(sent_at은 UTC 문자열로 저장되므로 같은 형식으로 바꿔 비교).
	// 날짜 문자열끼리 비교하면 서버 시간대에 따라 하루가 어긋난다.
	LastDeliveredContentID(ctx context.Context, repoSlug string, from, to time.Time) (contentID string, found bool, err error)
	AlreadySentToday(ctx context.Context, subscriberID int64, day time.Time) (bool, error)
	Record(ctx context.Context, subscriberID int64, repoSlug, contentID, channel string, sentAt time.Time) error
}

type sqlDeliveryLogStore struct{ db *sql.DB }

func NewDeliveryLogStore(db *sql.DB) DeliveryLogRepository { return &sqlDeliveryLogStore{db: db} }

func (s *sqlDeliveryLogStore) AlreadySentToday(ctx context.Context, subscriberID int64, day time.Time) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM delivery_log
		 WHERE subscriber_id = ?
		   AND date(sent_at) = date(?)
		 LIMIT 1`,
		subscriberID, day.Format("2006-01-02"),
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check delivery for %d: %w", subscriberID, err)
	}
	return true, nil
}

func (s *sqlDeliveryLogStore) LastDeliveredContentID(ctx context.Context, repoSlug string, from, to time.Time) (string, bool, error) {
	const layout = "2006-01-02 15:04:05" // Record가 저장하는 형식과 같아야 문자열 비교가 시간 비교가 된다
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT content_id FROM delivery_log
		 WHERE repo_slug = ? AND sent_at >= ? AND sent_at < ?
		 ORDER BY sent_at DESC, content_id DESC
		 LIMIT 1`,
		repoSlug, from.UTC().Format(layout), to.UTC().Format(layout),
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("last delivered content for %s: %w", repoSlug, err)
	}
	return id, true, nil
}

func (s *sqlDeliveryLogStore) Record(ctx context.Context, subscriberID int64, repoSlug, contentID, channel string, sentAt time.Time) error {
	if channel == "" {
		channel = "email"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO delivery_log(subscriber_id, repo_slug, content_id, channel, sent_at)
		VALUES (?, ?, ?, ?, ?)`,
		subscriberID, repoSlug, contentID, channel, sentAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("insert delivery_log: %w", err)
	}
	return nil
}
