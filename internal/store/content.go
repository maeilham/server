package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/maeilham/server/internal/pkg/closeutil"
)

type Content struct {
	RepoSlug         string
	ContentID        string
	Title            string
	Preview          string
	Tags             string // JSON array
	SourceURL        string
	SourceAuthor     string
	BodyPath         string
	GithubSHA        string
	DiscussionURL    string
	DiscussionNodeID string
	GitHubURL        string // populated by JOIN queries; not a contents column
	RotationCount    int
}

type ContentRepository interface {
	// Upsert inserts or updates a content row. Returns true if a new row was inserted.
	Upsert(ctx context.Context, c *Content) (inserted bool, err error)
	// ListByRepo returns lightweight rows (ContentID, GithubSHA, DiscussionNodeID) for sync diffing.
	ListByRepo(ctx context.Context, repoSlug string) ([]*Content, error)
	// MarkDeleted soft-deletes a single content row.
	MarkDeleted(ctx context.Context, repoSlug, contentID string) error
	// GetByID returns one content item by contentID (across all active repos).
	GetByID(ctx context.Context, contentID string) (*Content, error)
	// TodayForRepo returns the next-in-rotation content for a given repo.
	TodayForRepo(ctx context.Context, repoSlug string) (*Content, error)
	// Today returns the next-in-rotation content for the first active repo (lexicographic).
	Today(ctx context.Context) (*Content, error)
	// ListRecent returns the most recently synced contents across all active repos.
	ListRecent(ctx context.Context, limit int) ([]*Content, error)
	// AdvanceRotation bumps rotation_count for every (repo, content) that was delivered on day.
	AdvanceRotation(ctx context.Context, day time.Time) (int64, error)
	// SaveDiscussionURL persists the Discussion URL and node ID after creation.
	SaveDiscussionURL(ctx context.Context, repoSlug, contentID, url, nodeID string) error
}

type sqlContentStore struct{ db *sql.DB }

func NewContentStore(db *sql.DB) ContentRepository { return &sqlContentStore{db: db} }

func (s *sqlContentStore) Upsert(ctx context.Context, c *Content) (bool, error) {
	var existing sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT content_id FROM contents WHERE repo_slug = ? AND content_id = ?`,
		c.RepoSlug, c.ContentID,
	).Scan(&existing)

	switch {
	case err == sql.ErrNoRows:
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO contents
			  (repo_slug, content_id, title, preview, tags, source_url, source_author, body_path, github_sha)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.RepoSlug, c.ContentID, c.Title, c.Preview, c.Tags,
			nullableStr(c.SourceURL), nullableStr(c.SourceAuthor), c.BodyPath, c.GithubSHA,
		)
		if err != nil {
			return false, fmt.Errorf("insert content %s/%s: %w", c.RepoSlug, c.ContentID, err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("check content: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE contents
		   SET title         = ?,
		       preview       = ?,
		       tags          = ?,
		       source_url    = ?,
		       source_author = ?,
		       body_path     = ?,
		       github_sha    = ?,
		       deleted_at    = NULL,
		       synced_at     = CURRENT_TIMESTAMP
		 WHERE repo_slug = ? AND content_id = ?`,
		c.Title, c.Preview, c.Tags, nullableStr(c.SourceURL), nullableStr(c.SourceAuthor),
		c.BodyPath, c.GithubSHA, c.RepoSlug, c.ContentID,
	)
	return false, err
}

func (s *sqlContentStore) ListByRepo(ctx context.Context, repoSlug string) ([]*Content, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_id, COALESCE(github_sha,''), COALESCE(discussion_node_id,'')
		   FROM contents WHERE repo_slug = ? AND deleted_at IS NULL`,
		repoSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("list contents for %s: %w", repoSlug, err)
	}
	defer closeutil.Discard(rows)
	var out []*Content
	for rows.Next() {
		c := &Content{RepoSlug: repoSlug}
		if err := rows.Scan(&c.ContentID, &c.GithubSHA, &c.DiscussionNodeID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *sqlContentStore) MarkDeleted(ctx context.Context, repoSlug, contentID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE contents SET deleted_at = CURRENT_TIMESTAMP
		  WHERE repo_slug = ? AND content_id = ? AND deleted_at IS NULL`,
		repoSlug, contentID,
	)
	return err
}

func (s *sqlContentStore) GetByID(ctx context.Context, contentID string) (*Content, error) {
	var c Content
	err := s.db.QueryRowContext(ctx, `
		SELECT c.repo_slug, c.content_id, c.title, c.preview, c.body_path,
		       r.github_url, COALESCE(c.discussion_url,''), COALESCE(c.discussion_node_id,''), c.rotation_count
		  FROM contents c
		  JOIN repos r ON r.slug = c.repo_slug
		 WHERE c.content_id = ? AND c.deleted_at IS NULL AND r.active = 1
		 LIMIT 1`, contentID,
	).Scan(
		&c.RepoSlug, &c.ContentID, &c.Title, &c.Preview, &c.BodyPath,
		&c.GitHubURL, &c.DiscussionURL, &c.DiscussionNodeID, &c.RotationCount,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get content %s: %w", contentID, err)
	}
	return &c, nil
}

func (s *sqlContentStore) TodayForRepo(ctx context.Context, repoSlug string) (*Content, error) {
	var c Content
	err := s.db.QueryRowContext(ctx, `
		SELECT c.repo_slug, c.content_id, c.title, c.preview, c.body_path,
		       r.github_url, COALESCE(c.discussion_url,''), COALESCE(c.discussion_node_id,''), c.rotation_count
		  FROM contents c
		  JOIN repos r ON r.slug = c.repo_slug
		 WHERE c.repo_slug = ? AND c.deleted_at IS NULL
		 ORDER BY c.rotation_count ASC, c.content_id ASC
		 LIMIT 1`, repoSlug,
	).Scan(
		&c.RepoSlug, &c.ContentID, &c.Title, &c.Preview, &c.BodyPath,
		&c.GitHubURL, &c.DiscussionURL, &c.DiscussionNodeID, &c.RotationCount,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("today content for %s: %w", repoSlug, err)
	}
	return &c, nil
}

func (s *sqlContentStore) Today(ctx context.Context) (*Content, error) {
	var slug string
	err := s.db.QueryRowContext(ctx,
		`SELECT slug FROM repos WHERE active = 1 ORDER BY slug LIMIT 1`,
	).Scan(&slug)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query active repo: %w", err)
	}
	return s.TodayForRepo(ctx, slug)
}

func (s *sqlContentStore) ListRecent(ctx context.Context, limit int) ([]*Content, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.repo_slug, c.content_id, c.title, c.preview, c.body_path,
		       r.github_url, COALESCE(c.discussion_url,''), c.rotation_count
		  FROM contents c
		  JOIN repos r ON r.slug = c.repo_slug
		 WHERE c.deleted_at IS NULL AND r.active = 1
		 ORDER BY c.content_id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list contents: %w", err)
	}
	defer rows.Close()
	var out []*Content
	for rows.Next() {
		var c Content
		if err := rows.Scan(&c.RepoSlug, &c.ContentID, &c.Title, &c.Preview, &c.BodyPath,
			&c.GitHubURL, &c.DiscussionURL, &c.RotationCount); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *sqlContentStore) AdvanceRotation(ctx context.Context, day time.Time) (int64, error) {
	dayStr := day.Format("2006-01-02")
	res, err := s.db.ExecContext(ctx, `
		UPDATE contents
		   SET sent_at        = ?,
		       rotation_count = rotation_count + 1
		 WHERE (repo_slug, content_id) IN (
		     SELECT DISTINCT repo_slug, content_id
		       FROM delivery_log
		      WHERE date(sent_at) = date(?)
		 )
		   AND (sent_at IS NULL OR date(sent_at) != date(?))`,
		day.UTC().Format("2006-01-02 15:04:05"), dayStr, dayStr,
	)
	if err != nil {
		return 0, fmt.Errorf("advance rotation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (s *sqlContentStore) SaveDiscussionURL(ctx context.Context, repoSlug, contentID, url, nodeID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE contents SET discussion_url = ?, discussion_node_id = ? WHERE repo_slug = ? AND content_id = ?`,
		url, nodeID, repoSlug, contentID,
	)
	return err
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
