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

	Authored       int // 작성일(최초 커밋 시각)을 새로 채운 글 수
	AuthoredFailed int // GitHub 조회에 실패해 비워 둔 글 수 (다음 sync가 다시 시도)
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
		// 새 글이거나, 작성일 컬럼이 생기기 전부터 있던 글이면 작성일을 채워야 한다.
		needAuthored := !exists || prev.AuthoredAt.IsZero()
		if exists && prev.GithubSHA == e.SHA {
			if needAuthored { // 본문은 그대로여도 작성일은 채운다
				fillAuthoredAt(ctx, logger, contentStore, ghClient, owner, repoName, repoSlug, contentID, e.Path, stats)
			}
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
		if needAuthored {
			fillAuthoredAt(ctx, logger, contentStore, ghClient, owner, repoName, repoSlug, contentID, e.Path, stats)
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

// fillAuthoredAt는 글의 작성일(GitHub에서 그 파일의 최초 커밋 시각)을 조회해 저장한다.
// 조회에 실패해도 sync 자체는 성공으로 본다. NULL로 남겨 두면 다음 sync가 다시 시도하고,
// 그동안 목록은 synced_at으로 대체해 보여준다.
func fillAuthoredAt(
	ctx context.Context,
	logger *slog.Logger,
	contentStore store.ContentRepository,
	ghClient *GitHubClient,
	owner, repoName, repoSlug, contentID, path string,
	stats *SyncStats,
) {
	t, err := ghClient.FirstCommitDate(ctx, owner, repoName, path)
	if err != nil {
		logger.Warn("authored date lookup failed (will retry next sync)", "content", contentID, "err", err)
		stats.AuthoredFailed++
		return
	}
	if err := contentStore.SetAuthoredAt(ctx, repoSlug, contentID, t); err != nil {
		logger.Error("save authored date", "content", contentID, "err", err)
		stats.Errors++
		return
	}
	stats.Authored++
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
