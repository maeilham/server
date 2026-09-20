package content

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// commitsServer는 커밋 API 대역이다. pages[i]는 (i+1)번째 페이지에 돌려줄 커밋 날짜 목록이다.
type commitsServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
}

func newCommitsServer(t *testing.T, pages [][]string) *commitsServer {
	t.Helper()
	cs := &commitsServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.requests = append(cs.requests, r.Clone(context.Background()))
		cs.mu.Unlock()

		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		if page < 1 || page > len(pages) {
			fmt.Fprint(w, "[]")
			return
		}
		if len(pages) > 1 { // GitHub처럼 Link 헤더로 마지막 페이지를 알려준다
			base := cs.srv.URL + r.URL.Path + "?path=" + r.URL.Query().Get("path") + "&per_page=1"
			w.Header().Set("Link", fmt.Sprintf(`<%s&page=%d>; rel="next", <%s&page=%d>; rel="last"`, base, page+1, base, len(pages)))
		}
		var items []string
		for _, d := range pages[page-1] {
			items = append(items, fmt.Sprintf(`{"commit":{"author":{"date":%q}}}`, d))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *commitsServer) client(token string) *GitHubClient {
	return &GitHubClient{HTTP: cs.srv.Client(), Token: token, APIBase: cs.srv.URL}
}

func TestFirstCommitDate_SingleCommit(t *testing.T) {
	cs := newCommitsServer(t, [][]string{{"2026-09-01T09:00:00+09:00"}}) // Link 헤더 없음
	got, err := cs.client("tok").FirstCommitDate(context.Background(), "maeilham", "backend-ops", "content/0001-a.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v, want 2026-09-01T00:00:00Z (한국 시간 09:00과 같은 시각)", got)
	}
	if n := len(cs.requests); n != 1 {
		t.Errorf("요청 %d번, want 1 (커밋이 하나면 추가 요청 없음)", n)
	}

	r := cs.requests[0]
	if r.URL.Path != "/repos/maeilham/backend-ops/commits" {
		t.Errorf("path = %s", r.URL.Path)
	}
	if q := r.URL.Query(); q.Get("path") != "content/0001-a.md" || q.Get("per_page") != "1" {
		t.Errorf("query = %v", q)
	}
	if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("헤더 = %v", r.Header)
	}
}

func TestFirstCommitDate_FollowsLastPageForOldestCommit(t *testing.T) {
	// 최신순으로 5개: 1페이지가 최신, 5페이지가 가장 오래된 커밋
	cs := newCommitsServer(t, [][]string{
		{"2026-09-18T00:00:00Z"}, {"2026-09-15T00:00:00Z"}, {"2026-09-10T00:00:00Z"},
		{"2026-09-05T00:00:00Z"}, {"2026-08-01T00:00:00Z"},
	})
	got, err := cs.client("").FirstCommitDate(context.Background(), "o", "r", "content/0001-a.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v, want 가장 오래된 커밋(2026-08-01)", got)
	}
	if n := len(cs.requests); n != 2 {
		t.Errorf("요청 %d번, want 2 (첫 페이지 + 마지막 페이지)", n)
	}
	if p := cs.requests[1].URL.Query().Get("page"); p != "5" {
		t.Errorf("두 번째 요청 page = %q, want 5", p)
	}
	if cs.requests[0].Header.Get("Authorization") != "" {
		t.Error("토큰이 없으면 Authorization 헤더를 보내면 안 됨")
	}
}

func TestFirstCommitDate_NoCommitsIsNotFound(t *testing.T) {
	cs := newCommitsServer(t, nil) // 항상 []
	_, err := cs.client("").FirstCommitDate(context.Background(), "o", "r", "content/nope.md")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestFirstCommitDate_APIErrors(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"message":"boom"}`)
		}))
		c := &GitHubClient{HTTP: srv.Client(), APIBase: srv.URL}
		if _, err := c.FirstCommitDate(context.Background(), "o", "r", "p"); err == nil {
			t.Errorf("status %d: 오류가 나야 함", status)
		}
		srv.Close()
	}
}

func TestLastPage(t *testing.T) {
	cases := map[string]int{
		"": 0,
		`<https://api.github.com/x?page=2>; rel="next"`:                                        0, // last가 없음
		`<https://a/x?page=2>; rel="next", <https://a/x?path=p&per_page=1&page=7>; rel="last"`: 7,
		`<https://a/x?page=abc>; rel="last"`:                                                   0,
	}
	for link, want := range cases {
		if got := lastPage(link); got != want {
			t.Errorf("lastPage(%q) = %d, want %d", link, got, want)
		}
	}
}
