package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New는 level 문자열("debug"/"info"/"warn"/"error")에 맞는 slog.Logger를 반환한다.
// 알 수 없는 값이면 Info로 폴백한다.
func New(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
