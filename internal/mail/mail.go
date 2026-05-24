package mail

import (
	"context"
	"fmt"
	"log/slog"
)

type Message struct {
	To       string
	Subject  string
	HTMLBody string
	TextBody string
}

func (m Message) Validate() error {
	if m.To == "" {
		return fmt.Errorf("Message.To is required")
	}
	if m.Subject == "" {
		return fmt.Errorf("Message.Subject is required")
	}
	if m.HTMLBody == "" && m.TextBody == "" {
		return fmt.Errorf("Message body (HTML or text) is required")
	}
	return nil
}

type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// ConsoleMailer prints messages to the logger instead of sending. Used when no
// API key is configured (local dev, tests).
type ConsoleMailer struct {
	Logger *slog.Logger
	From   string
}

func (c *ConsoleMailer) Send(_ context.Context, msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	c.Logger.Info("mail (console)",
		"from", c.From, "to", msg.To, "subject", msg.Subject,
		"html_bytes", len(msg.HTMLBody), "text_bytes", len(msg.TextBody))
	return nil
}
