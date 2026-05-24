package content

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 안전 가드: 한 repo가 시스템에 영향을 못 주도록 상한선을 둠.
const (
	maxTreeEntries        = 5000             // Tree API 응답 안전선
	maxContentFiles       = 1000             // 우리가 동기화할 .md 상한
	maxFileBytes          = 64 * 1024        // 한 파일 최대 64KB (markdown으론 충분)
	httpTimeout           = 15 * time.Second
)

type GitHubClient struct {
	HTTP  *http.Client
	Token string // optional; raises rate limit + allows private repos
}

func NewGitHubClient(token string) *GitHubClient {
	return &GitHubClient{
		HTTP:  &http.Client{Timeout: httpTimeout},
		Token: token,
	}
}

type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

type treeResponse struct {
	Tree      []TreeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}

// ListTree fetches the full recursive tree at ref.
// Filters to content/*.md entries and enforces the safety caps above.
func (c *GitHubClient) ListTree(ctx context.Context, owner, repo, ref string) ([]TreeEntry, error) {
	if ref == "" {
		ref = "HEAD"
	}
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tree request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tree API status %d: %s", resp.StatusCode, string(body))
	}

	var tr treeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("tree decode: %w", err)
	}
	if tr.Truncated {
		return nil, fmt.Errorf("tree response truncated (repo too large)")
	}
	if len(tr.Tree) > maxTreeEntries {
		return nil, fmt.Errorf("tree has %d entries, limit is %d", len(tr.Tree), maxTreeEntries)
	}

	out := make([]TreeEntry, 0, len(tr.Tree))
	for _, e := range tr.Tree {
		if e.Type != "blob" {
			continue
		}
		if !strings.HasPrefix(e.Path, "content/") {
			continue
		}
		if !strings.HasSuffix(e.Path, ".md") {
			continue
		}
		if e.Size > maxFileBytes {
			// skip oversized files; do not fail entire sync
			continue
		}
		out = append(out, e)
		if len(out) > maxContentFiles {
			return nil, fmt.Errorf("repo has more than %d content files (abuse guard)", maxContentFiles)
		}
	}
	return out, nil
}

// FetchRaw downloads a single file from raw.githubusercontent.com.
// Reads at most maxFileBytes; anything larger is rejected.
func (c *GitHubClient) FetchRaw(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	if ref == "" {
		ref = "main"
	}
	u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref), path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("raw request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("raw status %d for %s", resp.StatusCode, path)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("raw read: %w", err)
	}
	if len(body) > maxFileBytes {
		return nil, fmt.Errorf("file %s exceeds %d bytes", path, maxFileBytes)
	}
	return body, nil
}

// ParseGitHubURL takes "https://github.com/<owner>/<repo>(.git)?" and extracts owner/repo.
func ParseGitHubURL(raw string) (owner, repo string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if u.Host != "github.com" {
		return "", "", fmt.Errorf("not a github.com URL: %s", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("URL path missing owner/repo: %s", raw)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}
