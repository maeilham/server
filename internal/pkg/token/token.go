package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func Make(email, secret string) string {
	exp := time.Now().Add(48 * time.Hour).Unix()
	msg := fmt.Sprintf("%s:%d", email, exp)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	payload := base64.RawURLEncoding.EncodeToString([]byte(msg))
	return payload + "." + sig
}

func Verify(tok, secret string) (string, error) {
	parts := strings.SplitN(tok, ".", 2)
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
