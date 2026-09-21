package token

import (
	"strings"
	"testing"
)

func TestNewAccessToken_FormatAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		tok, err := NewAccessToken()
		if err != nil {
			t.Fatal(err)
		}
		if !IsAccessToken(tok) {
			t.Fatalf("token %q is not in access-token format", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

func TestIsAccessToken(t *testing.T) {
	valid := strings.Repeat("a1", 32)
	cases := map[string]bool{
		valid:                                  true,
		"":                                     false,
		valid[:63]:                             false, // 짧음
		valid + "0":                            false, // 김
		strings.ToUpper(valid):                 false, // 대문자는 만들지 않는 형식
		strings.Repeat("g", 64):                false, // 16진수가 아님
		valid[:63] + " ":                       false,
		"a2lhaG9oaG9AbmF2ZXIuY29tOjE3OTAwNTU5": false, // 이메일이 든 HMAC 토큰 조각 같은 값
	}
	for in, want := range cases {
		if got := IsAccessToken(in); got != want {
			t.Errorf("IsAccessToken(%q) = %v, want %v", in, got, want)
		}
	}
}
