package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	imail "github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/subscriber"
)

type subscribeHandler struct {
	store   *subscriber.Store
	mailer  imail.Mailer
	baseURL string // 웹 프론트 URL (리다이렉트용)
	apiURL  string // API 서버 URL (메일 링크용)
	secret  string
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

	token := makeToken(req.Email, h.secret)
	confirmURL := fmt.Sprintf("%s/api/confirm?token=%s", h.apiURL, token)

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
	token := r.URL.Query().Get("token")
	email, err := verifyToken(token, h.secret)
	if err != nil {
		http.Redirect(w, r, h.baseURL+"/?status=invalid", http.StatusSeeOther)
		return
	}

	if err := h.store.Confirm(r.Context(), email); err != nil {
		http.Redirect(w, r, h.baseURL+"/?status=invalid", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, h.baseURL+"/?status=confirmed", http.StatusSeeOther)
}

func (h *subscribeHandler) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	email, err := verifyToken(token, h.secret)
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

// ── Token helpers ────────────────────────────────────────────────────────────

func makeToken(email, secret string) string {
	exp := time.Now().Add(48 * time.Hour).Unix()
	msg := fmt.Sprintf("%s:%d", email, exp)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	payload := base64.RawURLEncoding.EncodeToString([]byte(msg))
	return payload + "." + sig
}

func verifyToken(token, secret string) (string, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid token")
	}
	msgBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid token")
	}
	msg := string(msgBytes)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return "", fmt.Errorf("invalid signature")
	}

	idx := strings.LastIndex(msg, ":")
	if idx < 0 {
		return "", fmt.Errorf("invalid token format")
	}
	exp, err := strconv.ParseInt(msg[idx+1:], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid token expiry")
	}
	if time.Now().Unix() > exp {
		return "", fmt.Errorf("token expired")
	}
	return msg[:idx], nil
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
