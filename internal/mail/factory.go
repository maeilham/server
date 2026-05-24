package mail

import (
	"fmt"
	"log/slog"
	"strings"
)

// New returns a Mailer based on config. If apiKey is empty, returns a
// ConsoleMailer (logs only, no real send). Otherwise returns a ResendMailer.
func New(logger *slog.Logger, apiKey, fromEmail, fromName string) Mailer {
	from := fromAddress(fromEmail, fromName)
	if apiKey == "" {
		logger.Warn("no resend api key configured; using console mailer (no emails will be sent)")
		return &ConsoleMailer{Logger: logger, From: from}
	}
	return NewResendMailer(apiKey, from)
}

func fromAddress(email, name string) string {
	email = strings.TrimSpace(email)
	name = strings.TrimSpace(name)
	if name == "" {
		return email
	}
	return fmt.Sprintf("%s <%s>", name, email)
}
