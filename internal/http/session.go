package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

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

// handleMe는 GET /api/me를 처리한다. 부작용 없이 토큰 상태만 확인한다. 상태 코드: 200 / 401 / 500
func (h *sessionHandler) handleMe(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		jsonError(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}
	err := h.subSvc.SessionStatus(r.Context(), tok)
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
	jsonOK(w, meResponse{Status: "subscriber"})
}
