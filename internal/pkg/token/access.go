package token

import (
	"crypto/rand"
	"encoding/hex"
)

// AccessTokenLen은 개인 링크 토큰의 길이다(무작위 32바이트를 16진수로 쓴 64자).
const AccessTokenLen = 64

// NewAccessToken은 개인 링크에 쓰는 무작위 토큰을 만든다.
// Make/Verify의 HMAC 토큰과 달리 이메일이나 만료 시각이 들어 있지 않고, 서버가 DB에서 찾아 확인한다.
func NewAccessToken() (string, error) {
	b := make([]byte, AccessTokenLen/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IsAccessToken은 s가 접근 토큰의 형식(소문자 16진수 64자)인지 확인한다.
// DB를 찾기 전에 엉뚱한 값을 걸러내는 용도이고, 유효한 토큰인지는 DB가 판단한다.
func IsAccessToken(s string) bool {
	if len(s) != AccessTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
