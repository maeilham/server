package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	imail "github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/token"
	"github.com/maeilham/server/internal/subscriber"
)

type subscribeHandler struct {
	store   *subscriber.Store
	mailer  imail.Mailer
	baseURL string // 웹 프론트 URL (리다이렉트용)
	apiURL  string // API 서버 URL (메일 링크용)
	secret  string
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

	// 재구독 허용: 이전에 해지했어도 다시 구독 가능
	if err := h.store.Reactivate(r.Context(), req.Email); err != nil {
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}

	if _, err := h.store.Upsert(r.Context(), req.Email); err != nil {
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}

	tok := token.Make(req.Email, h.secret)
	confirmURL := fmt.Sprintf("%s/api/confirm?token=%s", h.apiURL, tok)

	subject, text, html := imail.RenderConfirm(confirmURL)
	msg := imail.Message{
		To:       req.Email,
		Subject:  subject,
		TextBody: text,
		HTMLBody:  html,
	}

	if err := h.mailer.Send(r.Context(), msg); err != nil {
		jsonError(w, "메일 발송 실패", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"message": "확인 메일을 발송했습니다"})
}

func (h *subscribeHandler) handleConfirm(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	email, err := token.Verify(tok, h.secret)
	if err != nil {
		h.logger.Warn("confirm: token verification failed", "err", err, "token_prefix", truncate(tok, 16))
		http.Redirect(w, r, h.baseURL+"/?status=invalid", http.StatusSeeOther)
		return
	}

	var repoSlugs []string
	if repos := r.URL.Query().Get("repos"); repos != "" {
		for _, s := range strings.Split(repos, ",") {
			if s = strings.TrimSpace(s); s != "" {
				repoSlugs = append(repoSlugs, s)
			}
		}
	}
	if err := h.store.Confirm(r.Context(), email, repoSlugs); err != nil {
		h.logger.Warn("confirm: db error", "err", err, "email", email)
		http.Redirect(w, r, h.baseURL+"/?status=invalid", http.StatusSeeOther)
		return
	}

	h.logger.Info("confirm: subscription confirmed", "email", email)
	http.Redirect(w, r, h.baseURL+"/?status=confirmed", http.StatusSeeOther)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (h *subscribeHandler) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	email, err := token.Verify(tok, h.secret)
	if err != nil {
		jsonError(w, "링크가 유효하지 않습니다", http.StatusBadRequest)
		return
	}

	if err := h.store.Unsubscribe(r.Context(), email); err != nil {
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
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
