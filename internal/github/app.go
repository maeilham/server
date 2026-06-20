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

type cachedToken struct {
	token   string
	expires time.Time
}

// App authenticates as a GitHub App and vends short-lived installation tokens.
type App struct {
	appID          int64
	installationID int64 // default installation
	privateKey     *rsa.PrivateKey
	http           *http.Client

	mu             sync.Mutex
	tokenCache     map[int64]cachedToken // installationID → token
	installCache   map[string]int64      // "owner/repo" → installationID
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
		tokenCache:     make(map[int64]cachedToken),
		installCache:   make(map[string]int64),
	}, nil
}

// Token returns a valid token for the default installation.
func (a *App) Token(ctx context.Context) (string, error) {
	return a.tokenForInstallation(ctx, a.installationID)
}

// TokenForRepo returns a valid token for the installation covering owner/repo.
func (a *App) TokenForRepo(ctx context.Context, owner, repo string) (string, error) {
	id, err := a.repoInstallationID(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return a.tokenForInstallation(ctx, id)
}

func (a *App) tokenForInstallation(ctx context.Context, installationID int64) (string, error) {
	a.mu.Lock()
	if c, ok := a.tokenCache[installationID]; ok && time.Now().Before(c.expires.Add(-2*time.Minute)) {
		a.mu.Unlock()
		return c.token, nil
	}
	a.mu.Unlock()

	jwt, err := a.makeJWT()
	if err != nil {
		return "", fmt.Errorf("make jwt: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", apiBase, installationID)
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

	a.mu.Lock()
	a.tokenCache[installationID] = cachedToken{token: result.Token, expires: result.ExpiresAt}
	a.mu.Unlock()

	return result.Token, nil
}

// repoInstallationID fetches the installation ID for a specific repo, with in-memory cache.
func (a *App) repoInstallationID(ctx context.Context, owner, repo string) (int64, error) {
	key := owner + "/" + repo

	a.mu.Lock()
	if id, ok := a.installCache[key]; ok {
		a.mu.Unlock()
		return id, nil
	}
	a.mu.Unlock()

	jwt, err := a.makeJWT()
	if err != nil {
		return 0, err
	}

	url := fmt.Sprintf("%s/repos/%s/%s/installation", apiBase, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("repo installation request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("repo installation status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("decode installation response: %w", err)
	}

	a.mu.Lock()
	a.installCache[key] = result.ID
	a.mu.Unlock()

	return result.ID, nil
}

// GraphQL executes a GraphQL query using the default installation token.
func (a *App) GraphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	token, err := a.Token(ctx)
	if err != nil {
		return err
	}
	return a.graphQL(ctx, token, query, variables, out)
}

// GraphQLForRepo executes a GraphQL query using the installation token for owner/repo.
func (a *App) GraphQLForRepo(ctx context.Context, owner, repo, query string, variables map[string]any, out any) error {
	token, err := a.TokenForRepo(ctx, owner, repo)
	if err != nil {
		return err
	}
	return a.graphQL(ctx, token, query, variables, out)
}

func (a *App) graphQL(ctx context.Context, token, query string, variables map[string]any, out any) error {
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
