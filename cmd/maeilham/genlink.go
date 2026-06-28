package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

type GenLinkCmd struct {
	Email   string `required:"" help:"대상 이메일"`
	Type    string `help:"링크 종류" default:"unsubscribe" enum:"unsubscribe,confirm"`
	BaseURL string `help:"웹 프론트 URL (미지정 시 환경변수)"`
	APIURL  string `help:"API 서버 URL (미지정 시 환경변수)"`
}

func (c *GenLinkCmd) Run(_ context.Context, d *deps) error {
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = d.cfg.BaseURL
	}
	apiURL := c.APIURL
	if apiURL == "" {
		apiURL = d.cfg.APIURL
	}

	token := makeHMACToken(c.Email, d.cfg.Secret)
	var link string
	switch c.Type {
	case "unsubscribe":
		link = fmt.Sprintf("%s/?action=unsubscribe&token=%s", strings.TrimSuffix(baseURL, "/"), token)
	case "confirm":
		link = fmt.Sprintf("%s/api/confirm?token=%s", strings.TrimSuffix(apiURL, "/"), token)
	}
	fmt.Println(link)
	return nil
}

func makeHMACToken(email, secret string) string {
	exp := time.Now().Add(48 * time.Hour).Unix()
	msg := fmt.Sprintf("%s:%d", email, exp)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	payload := base64.RawURLEncoding.EncodeToString([]byte(msg))
	return payload + "." + sig
}
