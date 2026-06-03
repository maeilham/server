package config

import (
	"fmt"
	"os"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    string
	BaseURL     string
	Secret      string

	GitHubToken string

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
		Secret:        getEnv("MAEILHAM_SECRET", "dev-secret-change-me"),
		GitHubToken:   os.Getenv("MAEILHAM_GITHUB_TOKEN"),
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
