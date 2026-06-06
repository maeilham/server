package github

import (
	"context"
	"fmt"
)

// RepoMeta fetches the repository node ID and available discussion categories.
func (a *App) RepoMeta(ctx context.Context, owner, repo string) (repoID string, categories map[string]string, err error) {
	var result struct {
		Repository struct {
			ID                  string `json:"id"`
			DiscussionCategories struct {
				Nodes []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"discussionCategories"`
		} `json:"repository"`
	}

	err = a.GraphQL(ctx, `
		query($owner: String!, $repo: String!) {
			repository(owner: $owner, name: $repo) {
				id
				discussionCategories(first: 20) {
					nodes { id name }
				}
			}
		}
	`, map[string]any{"owner": owner, "repo": repo}, &result)
	if err != nil {
		return "", nil, err
	}

	repoID = result.Repository.ID
	categories = make(map[string]string, len(result.Repository.DiscussionCategories.Nodes))
	for _, c := range result.Repository.DiscussionCategories.Nodes {
		categories[c.Name] = c.ID
	}
	return repoID, categories, nil
}

// CreateDiscussion opens a new Discussion and returns its URL.
func (a *App) CreateDiscussion(ctx context.Context, repoID, categoryID, title, body string) (string, error) {
	var result struct {
		CreateDiscussion struct {
			Discussion struct {
				URL string `json:"url"`
			} `json:"discussion"`
		} `json:"createDiscussion"`
	}

	err := a.GraphQL(ctx, `
		mutation($repoID: ID!, $categoryID: ID!, $title: String!, $body: String!) {
			createDiscussion(input: {
				repositoryId: $repoID
				categoryId:   $categoryID
				title:        $title
				body:         $body
			}) {
				discussion { url }
			}
		}
	`, map[string]any{
		"repoID":     repoID,
		"categoryID": categoryID,
		"title":      title,
		"body":       body,
	}, &result)
	if err != nil {
		return "", err
	}

	url := result.CreateDiscussion.Discussion.URL
	if url == "" {
		return "", fmt.Errorf("empty discussion URL in response")
	}
	return url, nil
}
