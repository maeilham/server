package content

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"regexp"

	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/store"
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
	contentStore store.ContentRepository,
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

	existing, err := contentStore.ListByRepo(ctx, repoSlug)
	if err != nil {
		return stats, fmt.Errorf("load current contents: %w", err)
	}
	current := make(map[string]*store.Content, len(existing))
	for _, c := range existing {
		current[c.ContentID] = c
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

		prev, exists := current[contentID]
		if exists && prev.GithubSHA == e.SHA {
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

		tagsJSON, err := encodeTags(parsed.Frontmatter.Tags)
		if err != nil {
			logger.Error("encode tags", "file", e.Path, "err", err)
			stats.Errors++
			continue
		}

		c := &store.Content{
			RepoSlug:  repoSlug,
			ContentID: contentID,
			Title:     parsed.Frontmatter.Title,
			Preview:   parsed.Frontmatter.Preview,
			Tags:      tagsJSON,
			BodyPath:  e.Path,
			GithubSHA: e.SHA,
		}

		inserted, err := contentStore.Upsert(ctx, c)
		if err != nil {
			logger.Error("upsert", "file", e.Path, "err", err)
			stats.Errors++
			continue
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Updated++
			if app != nil && exists && prev.DiscussionNodeID != "" {
				title := fmt.Sprintf("[매일함] %s", parsed.Frontmatter.Title)
				if err := app.UpdateDiscussionTitle(ctx, owner, repoName, prev.DiscussionNodeID, title); err != nil {
					logger.Warn("discussion title update failed (non-fatal)", "content", contentID, "err", err)
				}
			}
		}
	}

	for contentID := range current {
		if _, ok := seen[contentID]; ok {
			continue
		}
		if err := contentStore.MarkDeleted(ctx, repoSlug, contentID); err != nil {
			logger.Error("mark deleted", "content_id", contentID, "err", err)
			stats.Errors++
			continue
		}
		stats.Deleted++
	}

	return stats, nil
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
