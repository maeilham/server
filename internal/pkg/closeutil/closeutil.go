package closeutil

import (
	"io"
	"log/slog"
)

// Discard ignores the error from Close. Use for in-process resources like *sql.Rows.
func Discard(c io.Closer) {
	_ = c.Close()
}

// LogClose logs the error from Close at warn level via slog.Default().
// Use for external resources like HTTP response bodies.
func LogClose(what string, c io.Closer) {
	if err := c.Close(); err != nil {
		slog.Default().Warn("close error", "what", what, "err", err)
	}
}
