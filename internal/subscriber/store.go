package subscriber

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Upsert inserts a new subscriber or does nothing if already exists.
// Returns the subscriber's ID either way.
func (s *Store) Upsert(ctx context.Context, email string) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscribers (email) VALUES (?) ON CONFLICT(email) DO NOTHING`,
		email,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert subscriber: %w", err)
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM subscribers WHERE email = ?`, email).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get subscriber id: %w", err)
	}
	return id, nil
}

// Confirm sets confirmed_at and creates subscriptions.
// repoWeights maps repo slug to weight. If empty, subscribes to all active repos with default weight 3.
func (s *Store) Confirm(ctx context.Context, email string, repoWeights map[string]int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`UPDATE subscribers SET confirmed_at = ? WHERE email = ? AND confirmed_at IS NULL`,
		time.Now().UTC(), email,
	)
	if err != nil {
		return fmt.Errorf("confirm subscriber: %w", err)
	}

	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM subscribers WHERE email = ?`, email).Scan(&id); err != nil {
		return fmt.Errorf("get subscriber: %w", err)
	}

	if len(repoWeights) > 0 {
		for slug, weight := range repoWeights {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO subscriptions (subscriber_id, repo_slug, weight) VALUES (?, ?, ?)
				 ON CONFLICT(subscriber_id, repo_slug) DO UPDATE SET weight = excluded.weight`,
				id, slug, weight,
			); err != nil {
				return fmt.Errorf("create subscription for %s: %w", slug, err)
			}
		}
	} else {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subscriptions (subscriber_id, repo_slug, weight)
			SELECT ?, slug, 3 FROM repos WHERE active = 1
			ON CONFLICT DO NOTHING
		`, id)
		if err != nil {
			return fmt.Errorf("create subscriptions: %w", err)
		}
	}

	return tx.Commit()
}

// Reactivate clears unsubscribed_at so the email can re-subscribe.
func (s *Store) Reactivate(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = NULL, confirmed_at = NULL WHERE email = ? AND unsubscribed_at IS NOT NULL`,
		email,
	)
	return err
}

// Unsubscribe sets unsubscribed_at.
func (s *Store) Unsubscribe(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = ? WHERE email = ? AND unsubscribed_at IS NULL`,
		time.Now().UTC(), email,
	)
	return err
}

// IsUnsubscribed reports whether the email has unsubscribed.
func (s *Store) IsUnsubscribed(ctx context.Context, email string) (bool, error) {
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT unsubscribed_at FROM subscribers WHERE email = ?`, email).Scan(&t)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t.Valid, nil
}
