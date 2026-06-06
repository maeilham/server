package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    string
	BaseURL    string // 웹 프론트 URL (리다이렉트용)
	APIURL     string // API 서버 URL (메일 링크용)
	Secret     string

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
		HTTPAddr:      getEnv("MAEILHAM_HTTP_ADDR", ":8080"),
		DatabaseURL:   getEnv("MAEILHAM_DB", "./data/maeilham.db"),
		LogLevel:      getEnv("MAEILHAM_LOG_LEVEL", "info"),
		BaseURL:       getEnv("MAEILHAM_BASE_URL", "http://localhost:5173"),
		APIURL:        getEnv("MAEILHAM_API_URL", "http://localhost:8080"),
		Secret:        getEnv("MAEILHAM_SECRET", "dev-secret-change-me"),
		GitHubToken:          os.Getenv("MAEILHAM_GITHUB_TOKEN"),
		GitHubAppID:          parseInt64(os.Getenv("MAEILHAM_GITHUB_APP_ID")),
		GitHubAppPemPath:     getEnv("MAEILHAM_GITHUB_APP_PEM", "./maeilham-bot.pem"),
		GitHubInstallationID: parseInt64(os.Getenv("MAEILHAM_GITHUB_INSTALLATION_ID")),
		ResendAPIKey:  os.Getenv("MAEILHAM_RESEND_API_KEY"),
		MailFromEmail: getEnv("MAEILHAM_MAIL_FROM_EMAIL", "hello@maeilham.kr"),
		MailFromName:  getEnv("MAEILHAM_MAIL_FROM_NAME", "매일함"),
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
