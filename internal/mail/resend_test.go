package mail

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResendMailer_Send_Success(t *testing.T) {
	var capturedReq resendRequest
	var capturedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedReq)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc-123"}`))
	}))
	defer srv.Close()

	m := NewResendMailer("test-key", "매일함 <hello@maeilham.kr>")
	m.Endpoint = srv.URL

	err := m.Send(context.Background(), Message{
		To:       "user@example.com",
		Subject:  "오늘의 질문",
		HTMLBody: "<p>안녕</p>",
		TextBody: "안녕",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if capturedAuth != "Bearer test-key" {
		t.Errorf("auth header: got %q, want %q", capturedAuth, "Bearer test-key")
	}
	if capturedReq.From != "매일함 <hello@maeilham.kr>" {
		t.Errorf("from: got %q", capturedReq.From)
	}
	if len(capturedReq.To) != 1 || capturedReq.To[0] != "user@example.com" {
		t.Errorf("to: got %v", capturedReq.To)
	}
	if capturedReq.Subject != "오늘의 질문" {
		t.Errorf("subject: got %q", capturedReq.Subject)
	}
	if capturedReq.HTML != "<p>안녕</p>" {
		t.Errorf("html: got %q", capturedReq.HTML)
	}
	if capturedReq.Text != "안녕" {
		t.Errorf("text: got %q", capturedReq.Text)
	}
}

func TestResendMailer_Send_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"name":"missing_api_key","message":"API key is missing","statusCode":401}`))
	}))
	defer srv.Close()

	m := NewResendMailer("bad-key", "from@x.kr")
	m.Endpoint = srv.URL

	err := m.Send(context.Background(), Message{
		To: "u@x.kr", Subject: "s", TextBody: "t",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "missing_api_key") {
		t.Errorf("error should mention status and api error: %v", err)
	}
}

func TestResendMailer_Send_NoKey(t *testing.T) {
	m := NewResendMailer("", "from@x.kr")
	err := m.Send(context.Background(), Message{To: "u@x.kr", Subject: "s", TextBody: "t"})
	if err == nil {
		t.Fatal("expected error when API key is empty")
	}
}

func TestMessage_Validate(t *testing.T) {
	cases := []struct {
		name    string
		msg     Message
		wantErr bool
	}{
		{"valid html+text", Message{To: "u@x.kr", Subject: "s", HTMLBody: "<p>h</p>", TextBody: "t"}, false},
		{"html only", Message{To: "u@x.kr", Subject: "s", HTMLBody: "<p>h</p>"}, false},
		{"text only", Message{To: "u@x.kr", Subject: "s", TextBody: "t"}, false},
		{"missing to", Message{Subject: "s", TextBody: "t"}, true},
		{"missing subject", Message{To: "u@x.kr", TextBody: "t"}, true},
		{"missing body", Message{To: "u@x.kr", Subject: "s"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
