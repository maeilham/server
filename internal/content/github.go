package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maeilham/server/internal/pkg/closeutil"
)

// 안전 가드: 한 repo가 시스템에 영향을 못 주도록 상한선을 둠.
const (
	maxTreeEntries  = 5000      // Tree API 응답 안전선
	maxContentFiles = 1000      // 우리가 동기화할 .md 상한
	maxFileBytes    = 64 * 1024 // 한 파일 최대 64KB (markdown으론 충분)
	httpTimeout     = 15 * time.Second
)

// ErrNotFound는 GitHub에 해당 파일이 없을 때(삭제/이름 변경) FetchRaw가 돌려주는 오류다.
// 일시적 장애와 구분되어야 한다: 삭제된 글을 캐시된 옛 본문으로 계속 보여주면 안 된다.
var ErrNotFound = errors.New("content not found on github")

type GitHubClient struct {
	HTTP  *http.Client
	Token string // optional; raises rate limit + allows private repos

	// 비어 있으면 실제 GitHub를 쓴다. 테스트에서 가짜 서버로 바꾸기 위한 주입점이다.
	APIBase string // 기본 https://api.github.com
	RawBase string // 기본 https://raw.githubusercontent.com
}

func (c *GitHubClient) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimSuffix(c.APIBase, "/")
	}
	return "https://api.github.com"
}

func (c *GitHubClient) rawBase() string {
	if c.RawBase != "" {
		return strings.TrimSuffix(c.RawBase, "/")
	}
	return "https://raw.githubusercontent.com"
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
	u := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1",
		c.apiBase(), url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))

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
	defer closeutil.LogClose("github tree response", resp.Body)

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
	u := fmt.Sprintf("%s/%s/%s/%s/%s",
		c.rawBase(), url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref), path)

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
	defer closeutil.LogClose("github raw response", resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
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

type commitListItem struct {
	Commit struct {
		Author struct {
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

var linkLastRe = regexp.MustCompile(`<([^>]+)>;\s*rel="last"`)

// lastPage는 Link 헤더에서 rel="last"가 가리키는 페이지 번호를 꺼낸다. 없으면 0이다.
func lastPage(link string) int {
	m := linkLastRe.FindStringSubmatch(link)
	if m == nil {
		return 0
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(u.Query().Get("page"))
	if err != nil {
		return 0
	}
	return n
}

func (c *GitHubClient) fetchCommits(ctx context.Context, u string) ([]commitListItem, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("commits request: %w", err)
	}
	defer closeutil.LogClose("github commits response", resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("commits API status %d: %s", resp.StatusCode, string(body))
	}
	var items []commitListItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, "", fmt.Errorf("commits decode: %w", err)
	}
	return items, resp.Header.Get("Link"), nil
}

// FirstCommitDate returns the author date of the oldest commit that touched path.
//
// 커밋 API는 최신순으로만 돌려준다. 그래서 한 페이지에 커밋 1개(per_page=1)만 달라고 하면,
// 응답의 Link 헤더 rel="last"가 가리키는 마지막 페이지가 곧 가장 오래된 커밋이다.
// Link 헤더가 없으면 커밋이 하나뿐이라 처음 받은 것이 최초 커밋이다.
// 주의: API가 파일 이름 변경을 따라가지 않아, 이름을 바꾼 파일은 이름을 바꾼 커밋이 반환된다.
func (c *GitHubClient) FirstCommitDate(ctx context.Context, owner, repo, path string) (time.Time, error) {
	base := fmt.Sprintf("%s/repos/%s/%s/commits?path=%s&per_page=1",
		c.apiBase(), url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(path))

	commits, link, err := c.fetchCommits(ctx, base)
	if err != nil {
		return time.Time{}, err
	}
	if len(commits) == 0 {
		return time.Time{}, fmt.Errorf("%w: no commits for %s", ErrNotFound, path)
	}
	if last := lastPage(link); last > 1 {
		commits, _, err = c.fetchCommits(ctx, fmt.Sprintf("%s&page=%d", base, last))
		if err != nil {
			return time.Time{}, err
		}
		if len(commits) == 0 {
			return time.Time{}, fmt.Errorf("%w: no commits on last page for %s", ErrNotFound, path)
		}
	}
	return commits[0].Commit.Author.Date, nil
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
