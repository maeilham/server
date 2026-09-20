package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/maeilham/server/internal/content"
	"github.com/maeilham/server/internal/store"
)

// contentGetter는 이 핸들러가 필요로 하는 조회 기능만 좁힌 인터페이스다.
// store.ContentRepository가 만족하며, 테스트에서는 작은 대역으로 바꿀 수 있다.
type contentGetter interface {
	GetByRepoAndID(ctx context.Context, repoSlug, contentID string) (*store.Content, error)
}

// content_id는 파일명 앞 4자리 숫자다 (content/sync.go의 filenameRe와 같은 규칙).
var contentIDRe = regexp.MustCompile(`^\d{4}$`)

type contentHandler struct {
	contents contentGetter
	bodies   content.BodySource
	logger   *slog.Logger
}

// contentResponse는 웹의 콘텐츠 상세 화면이 쓰는 응답이다.
// 발송일·읽는 시간·댓글 수처럼 아직 서버에 없는 값은 넣지 않았다.
type contentResponse struct {
	Repo          string   `json:"repo"`     // repo slug (URL 경로에 쓰는 값)
	RepoName      string   `json:"repoName"` // 화면에 보여줄 이름
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Preview       string   `json:"preview"`
	Tags          []string `json:"tags"`
	Body          string   `json:"body"` // 마크다운 원문 (frontmatter 제외)
	DiscussionURL string   `json:"discussionUrl,omitempty"`
}

// handleGet은 GET /api/contents/{repo}/{id} 를 처리한다.
//
// content_id는 repo 안에서만 유일하므로 항상 (repo, id) 쌍으로 조회한다.
// 상태 코드: 200 / 404(없음, 삭제됨, 비활성 repo, 형식 오류) / 502(GitHub에서 본문을 못 가져옴)
func (h *contentHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	repo, id := chi.URLParam(r, "repo"), chi.URLParam(r, "id")
	if !contentIDRe.MatchString(id) {
		jsonError(w, "콘텐츠를 찾을 수 없습니다", http.StatusNotFound)
		return
	}

	c, err := h.contents.GetByRepoAndID(r.Context(), repo, id)
	if err != nil {
		h.logger.Error("get content", "repo", repo, "id", id, "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	if c == nil {
		jsonError(w, "콘텐츠를 찾을 수 없습니다", http.StatusNotFound)
		return
	}

	owner, name, err := content.ParseGitHubURL(c.GitHubURL)
	if err != nil {
		h.logger.Error("bad repo github url", "repo", repo, "url", c.GitHubURL, "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}

	body, err := h.bodies.Get(r.Context(), content.BodyRef{
		Owner: owner, Repo: name, Path: c.BodyPath, SHA: c.GithubSHA,
	})
	switch {
	case err == nil:
	case errors.Is(err, content.ErrNotFound):
		// DB에는 있지만 GitHub에서는 이미 지워진 글 (다음 sync 전)
		jsonError(w, "콘텐츠를 찾을 수 없습니다", http.StatusNotFound)
		return
	case errors.Is(err, context.Canceled):
		return // 클라이언트가 이미 떠났다
	default:
		h.logger.Warn("fetch content body", "repo", repo, "id", id, "err", err)
		jsonError(w, "본문을 가져오지 못했습니다", http.StatusBadGateway)
		return
	}

	jsonOK(w, contentResponse{
		Repo:          c.RepoSlug,
		RepoName:      c.RepoName,
		ID:            c.ContentID,
		Title:         c.Title,
		Preview:       c.Preview,
		Tags:          decodeTags(c.Tags),
		Body:          body,
		DiscussionURL: c.DiscussionURL,
	})
}

// decodeTags는 DB의 JSON 배열 문자열을 슬라이스로 푼다. 깨져 있어도 빈 배열을 돌려준다
// (웹이 null을 처리하지 않아도 되도록).
func decodeTags(raw string) []string {
	var tags []string
	if raw == "" || json.Unmarshal([]byte(raw), &tags) != nil || tags == nil {
		return []string{}
	}
	return tags
}
