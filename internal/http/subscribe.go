package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/maeilham/server/internal/subscriber"
)

type subscribeHandler struct {
	subSvc  *subscriber.SubscriberService
	baseURL string // 웹 프론트 URL (리다이렉트용)
	logger  *slog.Logger
}

func (h *subscribeHandler) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if _, err := mail.ParseAddress(req.Email); err != nil {
		jsonError(w, "이메일 형식이 올바르지 않습니다", http.StatusBadRequest)
		return
	}

	if err := h.subSvc.Subscribe(r.Context(), req.Email, nil); err != nil {
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"message": "확인 메일을 발송했습니다"})
}

func (h *subscribeHandler) handleConfirm(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	repoWeights := parseRepoWeights(r.URL.Query().Get("repos"))

	if err := h.subSvc.Confirm(r.Context(), tok, repoWeights); err != nil {
		h.logger.Warn("confirm failed", "err", err, "token_prefix", truncate(tok, 16))
		http.Redirect(w, r, h.baseURL+"/?status=invalid", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, h.baseURL+"/?status=confirmed", http.StatusSeeOther)
}

func (h *subscribeHandler) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if err := h.subSvc.Unsubscribe(r.Context(), tok); err != nil {
		jsonError(w, "링크가 유효하지 않습니다", http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

// parseRepoWeights parses "slug:weight,slug:weight" into a map.
// e.g. "be-interview:5,til:3"
func parseRepoWeights(raw string) map[string]int {
	if raw == "" {
		return nil
	}
	out := make(map[string]int)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		slug, wStr, _ := strings.Cut(part, ":")
		w := 3
		if n, err := strconv.Atoi(wStr); err == nil {
			w = n
		}
		out[slug] = w
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ── Response helpers ─────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
