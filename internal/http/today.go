package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/maeilham/server/internal/store"
)

// TodayPicker는 "오늘의 질문"을 정하는 쪽이다. delivery.TodayService가 구현한다.
type TodayPicker interface {
	// ForVisitor returns 오늘의 질문 하나. 보여줄 글이 하나도 없으면 (nil, nil).
	ForVisitor(ctx context.Context) (*store.Content, error)
}

type todayHandler struct {
	picker TodayPicker
	logger *slog.Logger
}

// todayItem은 오늘의 질문 하나다. 본문은 무거우므로 넣지 않고, 필요하면 repo/id로 상세 API를 부른다.
type todayItem struct {
	Repo     string   `json:"repo"`
	RepoName string   `json:"repoName"`
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Preview  string   `json:"preview"`
	Tags     []string `json:"tags"`
}

type todayResponse struct {
	Item todayItem `json:"item"`
}

// handleGet은 GET /api/today 를 처리한다. 오늘 발송해야 할 글 하나를 돌려준다(방문자용).
// 상태 코드: 200 / 404(보여줄 글이 하나도 없음) / 500
func (h *todayHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	c, err := h.picker.ForVisitor(r.Context())
	if err != nil {
		h.logger.Error("today question", "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	if c == nil {
		jsonError(w, "오늘의 질문이 아직 없습니다", http.StatusNotFound)
		return
	}
	jsonOK(w, todayResponse{Item: todayItem{
		Repo: c.RepoSlug, RepoName: c.RepoName, ID: c.ContentID,
		Title: c.Title, Preview: c.Preview, Tags: decodeTags(c.Tags),
	}})
}
