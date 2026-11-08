package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    string
	BaseURL     string // 웹 프론트 URL (리다이렉트용)
	APIURL      string // API 서버 URL (메일 링크용)
	Secret      string

	// TimeZone은 서비스의 "하루"를 나누는 시간대다(오늘의 질문). IANA 이름, 기본 Asia/Seoul.
	TimeZone string

	// SyncInterval이 0이면 serve가 콘텐츠를 sync하지 않는다(로컬 개발 기본).
	SyncInterval time.Duration

	// NotifyEnabled가 false이면 serve가 새 질문 알림을 보내지 않는다(로컬 개발 기본).
	// NotifyAt은 서비스 시간대 자정부터의 발송 시각(MAEILHAM_NOTIFY_AT=07:30)이다.
	NotifyEnabled bool
	NotifyAt      time.Duration

	GitHubToken string

	GitHubAppID          int64
	GitHubAppPemPath     string
	GitHubInstallationID int64

	ResendAPIKey  string
	MailFromEmail string
	MailFromName  string
}

func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:             getEnv("MAEILHAM_HTTP_ADDR", ":8080"),
		DatabaseURL:          getEnv("MAEILHAM_DB", "./data/maeilham.db"),
		LogLevel:             getEnv("MAEILHAM_LOG_LEVEL", "info"),
		BaseURL:              getEnv("MAEILHAM_BASE_URL", "http://localhost:5173"),
		APIURL:               getEnv("MAEILHAM_API_URL", "http://localhost:8080"),
		Secret:               getEnv("MAEILHAM_SECRET", "dev-secret-change-me"),
		TimeZone:             getEnv("MAEILHAM_TZ", "Asia/Seoul"),
		GitHubToken:          os.Getenv("MAEILHAM_GITHUB_TOKEN"),
		GitHubAppID:          parseInt64(os.Getenv("MAEILHAM_GITHUB_APP_ID")),
		GitHubAppPemPath:     getEnv("MAEILHAM_GITHUB_APP_PEM", "./maeilham-bot.pem"),
		GitHubInstallationID: parseInt64(os.Getenv("MAEILHAM_GITHUB_INSTALLATION_ID")),
		ResendAPIKey:         os.Getenv("MAEILHAM_RESEND_API_KEY"),
		MailFromEmail:        getEnv("MAEILHAM_MAIL_FROM_EMAIL", "hello@maeilham.kr"),
		MailFromName:         getEnv("MAEILHAM_MAIL_FROM_NAME", "매일함"),
	}

	if v := os.Getenv("MAEILHAM_SYNC_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("MAEILHAM_SYNC_INTERVAL %q: duration이어야 함(예: 1h, 비우면 끔)", v)
		}
		c.SyncInterval = d
	}

	if v := os.Getenv("MAEILHAM_NOTIFY_AT"); v != "" {
		t, err := time.Parse("15:04", v)
		if err != nil {
			return nil, fmt.Errorf("MAEILHAM_NOTIFY_AT %q: HH:MM 형식이어야 함(예: 07:30, 비우면 끔)", v)
		}
		c.NotifyEnabled = true
		c.NotifyAt = time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	}

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("MAEILHAM_DB is required")
	}
	return c, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
