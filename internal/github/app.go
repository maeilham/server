package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const apiBase = "https://api.github.com"

// App authenticates as a GitHub App and vends short-lived installation tokens.
type App struct {
	appID          int64
	installationID int64
	privateKey     *rsa.PrivateKey
	http           *http.Client

	mu      sync.Mutex
	cached  string
	expires time.Time
}

func NewApp(appID, installationID int64, pemPath string) (*App, error) {
	raw, err := os.ReadFile(pemPath)
	if err != nil {
		return nil, fmt.Errorf("read pem: %w", err)
	}
	key, err := parseRSAKey(raw)
	if err != nil {
		return nil, err
	}
	return &App{
		appID:          appID,
		installationID: installationID,
		privateKey:     key,
		http:           &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Token returns a valid installation access token, refreshing when needed.
func (a *App) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cached != "" && time.Now().Before(a.expires.Add(-2*time.Minute)) {
		return a.cached, nil
	}

	jwt, err := a.makeJWT()
	if err != nil {
		return "", fmt.Errorf("make jwt: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", apiBase, a.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("installation token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("installation token status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	a.cached = result.Token
	a.expires = result.ExpiresAt
	return a.cached, nil
}

// GraphQL executes a GraphQL query/mutation authenticated as the installation.
func (a *App) GraphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	token, err := a.Token(ctx)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("graphql request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql status %d: %s", resp.StatusCode, string(respBody))
	}

	var wrapper struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &wrapper); err != nil {
		return fmt.Errorf("graphql decode: %w", err)
	}
	if len(wrapper.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", wrapper.Errors[0].Message)
	}
	return json.Unmarshal(wrapper.Data, out)
}

// ── JWT ──────────────────────────────────────────────────────────────────────

func (a *App) makeJWT() (string, error) {
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	payloadJSON, err := json.Marshal(map[string]any{
		"iat": now.Unix() - 60,
		"exp": now.Unix() + 540,
		"iss": strconv.FormatInt(a.appID, 10),
	})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)

	msg := header + "." + payload
	h := sha256.New()
	h.Write([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.privateKey, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", err
	}
	return msg + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func parseRSAKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse rsa key: %w", err)
	}
	return key, nil
}
