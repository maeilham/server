package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/maeilham/server/internal/pkg/config"
)

// sync가 되풀이돼도 GitHub App은 한 번만 만든다(초기화 실패 경고도 한 번만 남는다).
func TestGhApp_IsInitializedOnlyOnce(t *testing.T) {
	var logs bytes.Buffer
	d := &deps{
		log: slog.New(slog.NewTextHandler(&logs, nil)),
		cfg: &config.Config{GitHubAppID: 1, GitHubInstallationID: 2, GitHubAppPemPath: "/nonexistent/app.pem"},
	}

	for i := 0; i < 3; i++ {
		if d.ghApp() != nil {
			t.Fatal("ghApp() = non-nil, want nil when the PEM cannot be read")
		}
	}
	if n := strings.Count(logs.String(), "github app init failed"); n != 1 {
		t.Errorf("init attempted %d times, want 1\n%s", n, logs.String())
	}
}

func TestGhApp_NilWhenNotConfigured(t *testing.T) {
	d := &deps{log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), cfg: &config.Config{}}
	if d.ghApp() != nil {
		t.Error("ghApp() = non-nil without GitHub App settings")
	}
}
