package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type DeliveryLogRepository interface {
	// AlreadySent reports whether subscriberID received any mail during [from, to) (서비스의 하루).
	AlreadySent(ctx context.Context, subscriberID int64, from, to time.Time) (bool, error)
	Record(ctx context.Context, subscriberID int64, repoSlug, contentID, channel string, sentAt time.Time) error
}

type sqlDeliveryLogStore struct{ db *sql.DB }

func NewDeliveryLogStore(db *sql.DB) DeliveryLogRepository { return &sqlDeliveryLogStore{db: db} }

func (s *sqlDeliveryLogStore) AlreadySent(ctx context.Context, subscriberID int64, from, to time.Time) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM delivery_log
		 WHERE subscriber_id = ?
		   AND sent_at >= ? AND sent_at < ?
		 LIMIT 1`,
		subscriberID, from.UTC().Format(timeLayout), to.UTC().Format(timeLayout),
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
		subscriberID, repoSlug, contentID, channel, sentAt.UTC().Format(timeLayout),
	)
	if err != nil {
		return fmt.Errorf("insert delivery_log: %w", err)
	}
	return nil
}
