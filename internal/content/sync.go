package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strings"

	"github.com/maeilham/server/internal/pkg/closeutil"
)

type SyncStats struct {
	Scanned  int
	Inserted int
	Updated  int
	Skipped  int
	Deleted  int
	Errors   int
}

// filename pattern (basename only): 0001-some-slug.md → content_id = "0001", send_order = 1
var filenameRe = regexp.MustCompile(`^(\d{4})-[a-z0-9-]+\.md$`)

// Sync diffs the repo's content/ tree against the DB and applies inserts/updates/deletes.
// Only files whose GitHub blob SHA differs from the stored one are fetched via raw URL.
func Sync(
	ctx context.Context,
	logger *slog.Logger,
	db *sql.DB,
	gh *GitHubClient,
	repoSlug, githubURL, ref string,
) (*SyncStats, error) {
	stats := &SyncStats{}

	owner, repoName, err := ParseGitHubURL(githubURL)
	if err != nil {
		return stats, fmt.Errorf("parse github url: %w", err)
	}

	tree, err := gh.ListTree(ctx, owner, repoName, ref)
	if err != nil {
		return stats, fmt.Errorf("list tree: %w", err)
	}

	current, err := loadCurrent(ctx, db, repoSlug)
	if err != nil {
		return stats, fmt.Errorf("load current contents: %w", err)
	}

	seen := make(map[string]struct{}, len(tree))
	for _, e := range tree {
		basename := path.Base(e.Path)
		match := filenameRe.FindStringSubmatch(basename)
		if match == nil {
			logger.Warn("skip file (bad name)", "file", e.Path)
			stats.Skipped++
			continue
		}
		stats.Scanned++

		contentID := match[1]
		seen[contentID] = struct{}{}

		if existingSHA, ok := current[contentID]; ok && existingSHA == e.SHA {
			continue // unchanged
		}

		raw, err := gh.FetchRaw(ctx, owner, repoName, ref, e.Path)
		if err != nil {
			logger.Error("fetch raw", "file", e.Path, "err", err)
			stats.Errors++
			continue
		}

		parsed, err := Parse(raw)
		if err != nil {
			logger.Error("parse frontmatter", "file", e.Path, "err", err)
			stats.Errors++
			continue
		}

		inserted, err := upsert(ctx, db, repoSlug, contentID, parsed, e.Path, e.SHA)
		if err != nil {
			logger.Error("upsert", "file", e.Path, "err", err)
			stats.Errors++
			continue
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Updated++
		}
	}

	for contentID := range current {
		if _, ok := seen[contentID]; ok {
			continue
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE contents SET deleted_at = CURRENT_TIMESTAMP
			 WHERE repo_slug = ? AND content_id = ? AND deleted_at IS NULL`,
			repoSlug, contentID,
		); err != nil {
			logger.Error("mark deleted", "content_id", contentID, "err", err)
			stats.Errors++
			continue
		}
		stats.Deleted++
	}

	return stats, nil
}

func loadCurrent(ctx context.Context, db *sql.DB, repoSlug string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT content_id, COALESCE(github_sha, '') FROM contents
		 WHERE repo_slug = ? AND deleted_at IS NULL`,
		repoSlug,
	)
	if err != nil {
		return nil, err
	}
	defer closeutil.Discard(rows)
	m := map[string]string{}
	for rows.Next() {
		var id, sha string
		if err := rows.Scan(&id, &sha); err != nil {
			return nil, err
		}
		m[id] = sha
	}
	return m, rows.Err()
}

// upsert returns true if a new row was inserted, false if an existing row was updated.
func upsert(
	ctx context.Context, db *sql.DB,
	repoSlug, contentID string, p *ParsedContent,
	bodyPath, githubSHA string,
) (bool, error) {
	tagsJSON, err := encodeTags(p.Frontmatter.Tags)
	if err != nil {
		return false, err
	}

	var sourceURL, sourceAuthor *string
	if p.Frontmatter.Source != nil {
		if s := strings.TrimSpace(p.Frontmatter.Source.URL); s != "" {
			sourceURL = &s
		}
		if s := strings.TrimSpace(p.Frontmatter.Source.Author); s != "" {
			sourceAuthor = &s
		}
	}

	var existing sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT content_id FROM contents WHERE repo_slug = ? AND content_id = ?`,
		repoSlug, contentID,
	).Scan(&existing)

	switch {
	case err == sql.ErrNoRows:
		_, err = db.ExecContext(ctx, `
			INSERT INTO contents
			  (repo_slug, content_id, title, preview, tags, source_url, source_author,
			   body_path, github_sha)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repoSlug, contentID, p.Frontmatter.Title, p.Frontmatter.Preview,
			tagsJSON, sourceURL, sourceAuthor, bodyPath, githubSHA)
		if err != nil {
			return false, err
		}
		return true, nil
	case err != nil:
		return false, err
	}

	_, err = db.ExecContext(ctx, `
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
		p.Frontmatter.Title, p.Frontmatter.Preview, tagsJSON, sourceURL, sourceAuthor,
		bodyPath, githubSHA, repoSlug, contentID)
	return false, err
}

func encodeTags(tags []string) (string, error) {
	if len(tags) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
