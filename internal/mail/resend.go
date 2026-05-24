package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultResendEndpoint = "https://api.resend.com/emails"

type ResendMailer struct {
	APIKey   string
	From     string // e.g. "매일함 <hello@maeilham.kr>"
	HTTP     *http.Client
	Endpoint string // override for tests; defaults to Resend production
}

var _ Mailer = (*ResendMailer)(nil)

func NewResendMailer(apiKey, from string) *ResendMailer {
	return &ResendMailer{
		APIKey: apiKey,
		From:   from,
		HTTP:   &http.Client{Timeout: 15 * time.Second},
	}
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html,omitempty"`
	Text    string   `json:"text,omitempty"`
}

type resendError struct {
	Name       string `json:"name"`
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
}

func (r *ResendMailer) Send(ctx context.Context, msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	if r.APIKey == "" {
		return fmt.Errorf("resend: API key is empty")
	}

	payload := resendRequest{
		From:    r.From,
		To:      []string{msg.To},
		Subject: msg.Subject,
		HTML:    msg.HTMLBody,
		Text:    msg.TextBody,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("resend: marshal: %w", err)
	}

	endpoint := r.Endpoint
	if endpoint == "" {
		endpoint = defaultResendEndpoint
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("resend: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("resend: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var apiErr resendError
	if jsonErr := json.Unmarshal(respBody, &apiErr); jsonErr == nil && apiErr.Message != "" {
		return fmt.Errorf("resend: %d %s: %s", resp.StatusCode, apiErr.Name, apiErr.Message)
	}
	return fmt.Errorf("resend: status %d: %s", resp.StatusCode, string(respBody))
}
