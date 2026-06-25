package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type DeliveryLogRepository interface {
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
