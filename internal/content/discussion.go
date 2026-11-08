package content

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/maeilham/server/internal/store"
)

// DiscussionCreator는 GitHub Discussion을 만드는 쪽임. *github.App이 구현함.
type DiscussionCreator interface {
	RepoMeta(ctx context.Context, owner, repo string) (repoID string, categories map[string]string, err error)
	CreateDiscussion(ctx context.Context, owner, repo, repoID, categoryID, title, body string) (url, nodeID string, err error)
}

// EnsureDiscussions는 repo의 삭제되지 않은 글 중 Discussion 주소가 없는 글마다 Discussion을 만듦.
// 글 하나가 실패해도 나머지는 계속하고, 실패한 글은 다음 sync가 다시 시도함.
func EnsureDiscussions(
	ctx context.Context,
	logger *slog.Logger,
	creator DiscussionCreator,
	contentStore store.ContentRepository,
	repo *store.Repo,
) (created, failed int, err error) {
	missing, err := contentStore.ListWithoutDiscussion(ctx, repo.Slug)
	if err != nil || len(missing) == 0 {
		return 0, 0, err
	}
	owner, name, err := ParseGitHubURL(repo.GitHubURL)
	if err != nil {
		return 0, 0, err
	}
	repoID, categories, err := creator.RepoMeta(ctx, owner, name)
	if err != nil {
		return 0, 0, fmt.Errorf("repo meta: %w", err)
	}
	categoryID := pickCategory(repo.DiscussionCategoryID, categories)
	if categoryID == "" {
		return 0, 0, errors.New("no discussion category found")
	}

	for _, c := range missing {
		title := fmt.Sprintf("[매일함] %s", c.Title)
		body := fmt.Sprintf("## %s\n\n%s\n\n---\n*매일함에서 자동으로 생성된 Discussion입니다. 자유롭게 답변을 달아주세요!*", c.Title, c.Preview)
		url, nodeID, err := creator.CreateDiscussion(ctx, owner, name, repoID, categoryID, title, body)
		if err != nil {
			logger.Warn("discussion create failed (non-fatal)", "content", c.ContentID, "err", err)
			failed++
			continue
		}
		// 저장에 실패하면 다음 sync가 같은 글에 또 만들게 되므로 여기서 멈춤
		if err := contentStore.SaveDiscussionURL(ctx, repo.Slug, c.ContentID, url, nodeID); err != nil {
			return created, failed, fmt.Errorf("save discussion url for %s: %w", c.ContentID, err)
		}
		created++
	}
	return created, failed, nil
}

// pickCategory는 repo에 지정된 카테고리를 쓰고, 없으면 General, 그것도 없으면 이름순 첫 카테고리를 씀.
func pickCategory(configured string, categories map[string]string) string {
	if configured != "" {
		return configured
	}
	if id, ok := categories["General"]; ok {
		return id
	}
	names := make([]string, 0, len(categories))
	for n := range categories {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return categories[names[0]]
}
