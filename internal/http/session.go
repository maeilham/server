package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/maeilham/server/internal/store"
	"github.com/maeilham/server/internal/subscriber"
)

// bearerToken은 "Authorization: Bearer <토큰>" 헤더에서 토큰을 뽑는다. 없거나 형식이 안 맞으면 ok=false.
func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	tok := strings.TrimPrefix(auth, prefix)
	if tok == "" {
		return "", false
	}
	return tok, true
}

type sessionHandler struct {
	subSvc *subscriber.SubscriberService
	logger *slog.Logger
}

type sessionResponse struct {
	Status         string `json:"status"`
	NewlyConfirmed bool   `json:"newly_confirmed"`
}

type meResponse struct {
	Status string `json:"status"`
	Email  string `json:"email"`
}

// handleSession은 POST /api/session을 처리한다. 개인 링크를 처음 열면 가입을 완료시키고,
// 이미 확인된 사람이 다시 열면 아무것도 바꾸지 않는다(멱등). 상태 코드: 200 / 401 / 500
func (h *sessionHandler) handleSession(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		jsonError(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}
	newly, err := h.subSvc.EstablishSession(r.Context(), tok)
	switch {
	case err == nil:
	case errors.Is(err, subscriber.ErrUnauthorized):
		jsonError(w, "유효하지 않은 링크입니다", http.StatusUnauthorized)
		return
	default:
		h.logger.Error("establish session", "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	jsonOK(w, sessionResponse{Status: "subscriber", NewlyConfirmed: newly})
}

// handleMe는 GET /api/me를 처리한다. 부작용 없이 토큰 상태를 확인하고 이메일을 돌려준다. 상태 코드: 200 / 401 / 500
func (h *sessionHandler) handleMe(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		jsonError(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}
	email, err := h.subSvc.SessionStatus(r.Context(), tok)
	switch {
	case err == nil:
	case errors.Is(err, subscriber.ErrUnauthorized):
		jsonError(w, "유효하지 않은 링크입니다", http.StatusUnauthorized)
		return
	default:
		h.logger.Error("session status", "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	jsonOK(w, meResponse{Status: "subscriber", Email: email})
}

type subscriptionItem struct {
	Repo        string `json:"repo"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type subscriptionsResponse struct {
	Items []subscriptionItem `json:"items"`
}

// handleSubscriptions는 GET /api/me/subscriptions를 처리한다. 활성 repo 전체와 내 구독 여부를 돌려준다.
// 상태 코드: 200 / 401 / 500
func (h *sessionHandler) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		jsonError(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}
	list, err := h.subSvc.RepoSubscriptions(r.Context(), tok)
	switch {
	case err == nil:
	case errors.Is(err, subscriber.ErrUnauthorized):
		jsonError(w, "유효하지 않은 링크입니다", http.StatusUnauthorized)
		return
	default:
		h.logger.Error("repo subscriptions", "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	items := make([]subscriptionItem, 0, len(list))
	for _, rs := range list {
		items = append(items, subscriptionItem{Repo: rs.Slug, Name: rs.Name, Description: rs.Description, Enabled: rs.Enabled})
	}
	jsonOK(w, subscriptionsResponse{Items: items})
}

type setSubscriptionResponse struct {
	Repo    string `json:"repo"`
	Enabled bool   `json:"enabled"`
}

// handleSetSubscription은 PUT /api/me/subscriptions/{repo}를 처리한다. 본문은 {"enabled": true|false}이고
// 멱등하다. 상태 코드: 200 / 400(본문 오류) / 401 / 404(없거나 비활성인 repo) / 500
func (h *sessionHandler) handleSetSubscription(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		jsonError(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		jsonError(w, "enabled(true/false)가 필요합니다", http.StatusBadRequest)
		return
	}
	slug := chi.URLParam(r, "repo")
	err := h.subSvc.SetRepoSubscription(r.Context(), tok, slug, *req.Enabled)
	switch {
	case err == nil:
	case errors.Is(err, subscriber.ErrUnauthorized):
		jsonError(w, "유효하지 않은 링크입니다", http.StatusUnauthorized)
		return
	case errors.Is(err, store.ErrRepoNotFound):
		jsonError(w, "없는 분야입니다", http.StatusNotFound)
		return
	default:
		h.logger.Error("set subscription", "repo", slug, "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	jsonOK(w, setSubscriptionResponse{Repo: slug, Enabled: *req.Enabled})
}

// handleUnsubscribeMe는 POST /api/me/unsubscribe를 처리한다. 개인 링크 토큰으로 구독을 해지한다.
// 해지 뒤에는 그 토큰이 401이라 같은 요청을 다시 보내도 401이다. 상태 코드: 200 / 401 / 500
func (h *sessionHandler) handleUnsubscribeMe(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		jsonError(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}
	err := h.subSvc.UnsubscribeSession(r.Context(), tok)
	switch {
	case err == nil:
	case errors.Is(err, subscriber.ErrUnauthorized):
		jsonError(w, "유효하지 않은 링크입니다", http.StatusUnauthorized)
		return
	default:
		h.logger.Error("unsubscribe session", "err", err)
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"message": "구독이 해지되었습니다"})
}
