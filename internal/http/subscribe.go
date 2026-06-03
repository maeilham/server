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

	"github.com/maeilham/server/internal/subscriber"
	imail "github.com/maeilham/server/internal/mail"
)

type subscribeHandler struct {
	store   *subscriber.Store
	mailer  imail.Mailer
	baseURL string
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

	unsub, err := h.store.IsUnsubscribed(r.Context(), req.Email)
	if err != nil {
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	if unsub {
		jsonError(w, "수신 거부 처리된 이메일입니다", http.StatusConflict)
		return
	}

	if _, err := h.store.Upsert(r.Context(), req.Email); err != nil {
		jsonError(w, "서버 오류", http.StatusInternalServerError)
		return
	}

	token := makeToken(req.Email, h.secret)
	confirmURL := fmt.Sprintf("%s/api/confirm?token=%s", h.baseURL, token)

	msg := imail.Message{
		To:      req.Email,
		Subject: "매일함 구독을 확인해주세요",
		HTMLBody: fmt.Sprintf(`<p>안녕하세요! 아래 버튼을 눌러 구독을 완료해주세요.</p>
<p><a href="%s" style="background:#1a1108;color:#F5EFDF;padding:12px 24px;text-decoration:none;display:inline-block;">구독 확인하기</a></p>
<p style="color:#aaa;font-size:12px;">이 메일을 요청하지 않으셨다면 무시해주세요. 링크는 48시간 후 만료됩니다.</p>`, confirmURL),
		TextBody: fmt.Sprintf("매일함 구독 확인 링크: %s\n\n48시간 후 만료됩니다.", confirmURL),
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
		http.Redirect(w, r, h.baseURL+"/confirm?status=invalid", http.StatusSeeOther)
		return
	}

	if err := h.store.Confirm(r.Context(), email); err != nil {
		http.Redirect(w, r, h.baseURL+"/confirm?status=error", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, h.baseURL+"/confirm?status=ok", http.StatusSeeOther)
}

func (h *subscribeHandler) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	email, err := verifyToken(token, h.secret)
	if err != nil {
		http.Redirect(w, r, h.baseURL+"/unsubscribe?status=invalid", http.StatusSeeOther)
		return
	}

	if err := h.store.Unsubscribe(r.Context(), email); err != nil {
		http.Redirect(w, r, h.baseURL+"/unsubscribe?status=error", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, h.baseURL+"/unsubscribe?status=ok", http.StatusSeeOther)
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
