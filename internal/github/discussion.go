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

	err = a.GraphQLForRepo(ctx, owner, repo, `
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

// CreateDiscussion opens a new Discussion and returns its URL and node ID.
func (a *App) CreateDiscussion(ctx context.Context, owner, repo, repoID, categoryID, title, body string) (url, nodeID string, err error) {
	var result struct {
		CreateDiscussion struct {
			Discussion struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"discussion"`
		} `json:"createDiscussion"`
	}

	err = a.GraphQLForRepo(ctx, owner, repo, `
		mutation($repoID: ID!, $categoryID: ID!, $title: String!, $body: String!) {
			createDiscussion(input: {
				repositoryId: $repoID
				categoryId:   $categoryID
				title:        $title
				body:         $body
			}) {
				discussion { id url }
			}
		}
	`, map[string]any{
		"repoID":     repoID,
		"categoryID": categoryID,
		"title":      title,
		"body":       body,
	}, &result)
	if err != nil {
		return "", "", err
	}

	url = result.CreateDiscussion.Discussion.URL
	if url == "" {
		return "", "", fmt.Errorf("empty discussion URL in response")
	}
	return url, result.CreateDiscussion.Discussion.ID, nil
}

// FetchDiscussion fetches a discussion's node ID, body, and comments by number.
func (a *App) FetchDiscussion(ctx context.Context, owner, repo string, number int) (*Discussion, error) {
	var result struct {
		Repository struct {
			Discussion struct {
				ID       string `json:"id"`
				Body     string `json:"body"`
				Comments struct {
					Nodes []struct {
						Body   string `json:"body"`
						Author struct {
							Login string `json:"login"`
						} `json:"author"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"discussion"`
		} `json:"repository"`
	}

	err := a.GraphQLForRepo(ctx, owner, repo, `
		query($owner: String!, $repo: String!, $number: Int!) {
			repository(owner: $owner, name: $repo) {
				discussion(number: $number) {
					id
					body
					comments(first: 100) {
						nodes {
							body
							author { login }
						}
					}
				}
			}
		}
	`, map[string]any{"owner": owner, "repo": repo, "number": number}, &result)
	if err != nil {
		return nil, err
	}

	d := result.Repository.Discussion
	comments := make([]DiscussionComment, len(d.Comments.Nodes))
	for i, n := range d.Comments.Nodes {
		comments[i] = DiscussionComment{Author: n.Author.Login, Body: n.Body}
	}
	return &Discussion{
		NodeID:   d.ID,
		Body:     d.Body,
		Comments: comments,
	}, nil
}

// UpdateDiscussionBody replaces the body of an existing Discussion.
func (a *App) UpdateDiscussionBody(ctx context.Context, owner, repo, nodeID, body string) error {
	var result struct {
		UpdateDiscussion struct {
			Discussion struct {
				ID string `json:"id"`
			} `json:"discussion"`
		} `json:"updateDiscussion"`
	}
	return a.GraphQLForRepo(ctx, owner, repo, `
		mutation($nodeID: ID!, $body: String!) {
			updateDiscussion(input: {
				discussionId: $nodeID
				body:         $body
			}) {
				discussion { id }
			}
		}
	`, map[string]any{"nodeID": nodeID, "body": body}, &result)
}

// UpdateDiscussionTitle updates only the title of an existing Discussion.
func (a *App) UpdateDiscussionTitle(ctx context.Context, owner, repo, nodeID, title string) error {
	var result struct {
		UpdateDiscussion struct {
			Discussion struct {
				ID string `json:"id"`
			} `json:"discussion"`
		} `json:"updateDiscussion"`
	}

	return a.GraphQLForRepo(ctx, owner, repo, `
		mutation($nodeID: ID!, $title: String!) {
			updateDiscussion(input: {
				discussionId: $nodeID
				title:        $title
			}) {
				discussion { id }
			}
		}
	`, map[string]any{
		"nodeID": nodeID,
		"title":  title,
	}, &result)
}
