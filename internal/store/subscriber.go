package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/maeilham/server/internal/pkg/closeutil"
)

type Subscriber struct {
	ID    int64
	Email string
}

type Subscription struct {
	RepoSlug string
	Weight   int
}

// SubscriberRepository는 subscribers/subscriptions 테이블 단일 쿼리 메서드와
// 여러 쿼리를 하나의 트랜잭션으로 묶는 WithTx를 제공한다.
type SubscriberRepository interface {
	// WithTx runs fn within a transaction. Nested calls reuse the existing transaction.
	WithTx(ctx context.Context, fn func(SubscriberRepository) error) error

	Upsert(ctx context.Context, email string) (int64, error)
	SetConfirmed(ctx context.Context, email string) (int64, error)
	ClearSubscriptions(ctx context.Context, id int64) error
	AddSubscription(ctx context.Context, id int64, slug string, weight int) error
	AddAllActiveRepoSubscriptions(ctx context.Context, id int64) error
	Reactivate(ctx context.Context, email string) error
	Unsubscribe(ctx context.Context, email string) error
	IsUnsubscribed(ctx context.Context, email string) (bool, error)
	ListActive(ctx context.Context) ([]Subscriber, error)
	IsActive(ctx context.Context, id int64) (bool, error)
	LoadSubscriptions(ctx context.Context, id int64) ([]Subscription, error)
}

// subQueries holds all single-query method implementations, shared by both store types.
type subQueries struct{ db dbtx }

// sqlSubscriberStore uses *sql.DB and can start new transactions via WithTx.
type sqlSubscriberStore struct {
	subQueries
	sqlDB *sql.DB
}

// txSubscriberStore uses *sql.Tx; WithTx reuses the existing transaction.
type txSubscriberStore struct {
	subQueries
}

func NewSubscriberStore(db *sql.DB) SubscriberRepository {
	return &sqlSubscriberStore{subQueries: subQueries{db: db}, sqlDB: db}
}

func (s *sqlSubscriberStore) WithTx(ctx context.Context, fn func(SubscriberRepository) error) error {
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(&txSubscriberStore{subQueries: subQueries{db: tx}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *txSubscriberStore) WithTx(_ context.Context, fn func(SubscriberRepository) error) error {
	return fn(s) // already in a transaction; reuse it
}

// ── Single-query methods on subQueries ───────────────────────────────────────

func (s *subQueries) Upsert(ctx context.Context, email string) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscribers (email) VALUES (?) ON CONFLICT(email) DO NOTHING`, email,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert subscriber: %w", err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM subscribers WHERE email = ?`, email,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("get subscriber id: %w", err)
	}
	return id, nil
}

func (s *subQueries) SetConfirmed(ctx context.Context, email string) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET confirmed_at = ? WHERE email = ? AND confirmed_at IS NULL`,
		time.Now().UTC(), email,
	)
	if err != nil {
		return 0, fmt.Errorf("set confirmed_at: %w", err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM subscribers WHERE email = ?`, email,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("get subscriber id: %w", err)
	}
	return id, nil
}

func (s *subQueries) ClearSubscriptions(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE subscriber_id = ?`, id)
	if err != nil {
		return fmt.Errorf("clear subscriptions: %w", err)
	}
	return nil
}

func (s *subQueries) AddSubscription(ctx context.Context, id int64, slug string, weight int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriptions (subscriber_id, repo_slug, weight) VALUES (?, ?, ?)`,
		id, slug, weight,
	)
	if err != nil {
		return fmt.Errorf("add subscription %s: %w", slug, err)
	}
	return nil
}

func (s *subQueries) AddAllActiveRepoSubscriptions(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriptions (subscriber_id, repo_slug, weight)
		 SELECT ?, slug, 3 FROM repos WHERE active = 1`, id,
	)
	if err != nil {
		return fmt.Errorf("add all active repo subscriptions: %w", err)
	}
	return nil
}

func (s *subQueries) Reactivate(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = NULL, confirmed_at = NULL
		  WHERE email = ? AND unsubscribed_at IS NOT NULL`, email,
	)
	return err
}

func (s *subQueries) Unsubscribe(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = ? WHERE email = ? AND unsubscribed_at IS NULL`,
		time.Now().UTC(), email,
	)
	return err
}

func (s *subQueries) IsUnsubscribed(ctx context.Context, email string) (bool, error) {
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT unsubscribed_at FROM subscribers WHERE email = ?`, email,
	).Scan(&t)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t.Valid, nil
}

func (s *subQueries) ListActive(ctx context.Context) ([]Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email FROM subscribers
		 WHERE confirmed_at IS NOT NULL
		   AND paused_at IS NULL
		   AND unsubscribed_at IS NULL
		 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer closeutil.Discard(rows)
	var out []Subscriber
	for rows.Next() {
		var s Subscriber
		if err := rows.Scan(&s.ID, &s.Email); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *subQueries) IsActive(ctx context.Context, id int64) (bool, error) {
	var confirmed, paused, unsubscribed sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT confirmed_at, paused_at, unsubscribed_at FROM subscribers WHERE id = ?`, id,
	).Scan(&confirmed, &paused, &unsubscribed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load subscriber %d: %w", id, err)
	}
	return confirmed.Valid && !paused.Valid && !unsubscribed.Valid, nil
}

func (s *subQueries) LoadSubscriptions(ctx context.Context, id int64) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.repo_slug, s.weight
		  FROM subscriptions s
		  JOIN repos r ON r.slug = s.repo_slug
		 WHERE s.subscriber_id = ? AND r.active = 1
		 ORDER BY s.repo_slug`, id)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}
	defer closeutil.Discard(rows)
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.RepoSlug, &sub.Weight); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}
