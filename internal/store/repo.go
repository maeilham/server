package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/maeilham/server/internal/pkg/closeutil"
)

type Repo struct {
	Slug                 string
	GitHubURL            string
	DisplayName          string
	Description          string
	Active               bool
	DiscussionCategoryID string
}

type RepoRepository interface {
	ListActive(ctx context.Context) ([]*Repo, error)
	ListAll(ctx context.Context) ([]*Repo, error)
	Upsert(ctx context.Context, r *Repo) error
	Deactivate(ctx context.Context, slug string) error
}

type sqlRepoStore struct{ db *sql.DB }

func NewRepoStore(db *sql.DB) RepoRepository { return &sqlRepoStore{db: db} }

func (s *sqlRepoStore) ListActive(ctx context.Context) ([]*Repo, error) {
	return s.query(ctx, `WHERE active = 1`)
}

func (s *sqlRepoStore) ListAll(ctx context.Context) ([]*Repo, error) {
	return s.query(ctx, "")
}

func (s *sqlRepoStore) query(ctx context.Context, where string) ([]*Repo, error) {
	q := `SELECT slug, github_url, display_name, COALESCE(description,''), active, COALESCE(discussion_category_id,'')
	        FROM repos ` + where + ` ORDER BY slug`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer closeutil.Discard(rows)
	var out []*Repo
	for rows.Next() {
		var r Repo
		var active int
		if err := rows.Scan(&r.Slug, &r.GitHubURL, &r.DisplayName, &r.Description, &active, &r.DiscussionCategoryID); err != nil {
			return nil, err
		}
		r.Active = active == 1
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *sqlRepoStore) Upsert(ctx context.Context, r *Repo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repos (slug, github_url, display_name, description, active)
		 VALUES (?, ?, ?, ?, 1)
		 ON CONFLICT(slug) DO UPDATE SET
		   github_url   = excluded.github_url,
		   display_name = excluded.display_name,
		   description  = excluded.description,
		   active       = 1`,
		r.Slug, r.GitHubURL, r.DisplayName, r.Description,
	)
	if err != nil {
		return fmt.Errorf("upsert repo %s: %w", r.Slug, err)
	}
	return nil
}

func (s *sqlRepoStore) Deactivate(ctx context.Context, slug string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repos SET active = 0 WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("deactivate repo %s: %w", slug, err)
	}
	return nil
}
