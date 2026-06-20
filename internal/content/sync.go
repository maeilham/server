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

	gh "github.com/maeilham/server/internal/github"
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
// If app is non-nil, Discussion titles are updated when content changes.
func Sync(
	ctx context.Context,
	logger *slog.Logger,
	db *sql.DB,
	ghClient *GitHubClient,
	app *gh.App,
	repoSlug, githubURL, ref string,
) (*SyncStats, error) {
	stats := &SyncStats{}

	owner, repoName, err := ParseGitHubURL(githubURL)
	if err != nil {
		return stats, fmt.Errorf("parse github url: %w", err)
	}

	tree, err := ghClient.ListTree(ctx, owner, repoName, ref)
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

		existing, exists := current[contentID]
		if exists && existing.sha == e.SHA {
			continue // unchanged
		}

		raw, err := ghClient.FetchRaw(ctx, owner, repoName, ref, e.Path)
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
			if app != nil && existing.discussionNodeID != "" {
				title := fmt.Sprintf("[매일함] %s", parsed.Frontmatter.Title)
				if err := app.UpdateDiscussionTitle(ctx, owner, repoName, existing.discussionNodeID, title); err != nil {
					logger.Warn("discussion title update failed (non-fatal)", "content", contentID, "err", err)
				}
			}
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

type currentEntry struct {
	sha              string
	discussionNodeID string
}

func loadCurrent(ctx context.Context, db *sql.DB, repoSlug string) (map[string]currentEntry, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT content_id, COALESCE(github_sha, ''), COALESCE(discussion_node_id, '')
		 FROM contents WHERE repo_slug = ? AND deleted_at IS NULL`,
		repoSlug,
	)
	if err != nil {
		return nil, err
	}
	defer closeutil.Discard(rows)
	m := map[string]currentEntry{}
	for rows.Next() {
		var id string
		var e currentEntry
		if err := rows.Scan(&id, &e.sha, &e.discussionNodeID); err != nil {
			return nil, err
		}
		m[id] = e
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
