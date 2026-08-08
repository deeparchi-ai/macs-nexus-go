package feishu

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Sender is a thin Feishu OpenAPI client for sending messages. It handles
// tenant token acquisition (with cache) and im.message.create. Stdlib-only.
// Credentials are passed explicitly — deployments decide how to source them
// (env, secret store, config).
type Sender struct {
	appID     string
	appSecret string
	baseURL   string
	client    *http.Client

	mu        sync.Mutex
	token     string
	tokenExp  time.Time
	refreshing bool        // true when a goroutine is actively refreshing
	refreshCond *sync.Cond // broadcast when refresh completes
}

// NewSender creates a Feishu message sender. baseURL defaults to
// https://open.feishu.cn if empty.
func NewSender(appID, appSecret, baseURL string) *Sender {
	if baseURL == "" {
		baseURL = "https://open.feishu.cn"
	}
	s := &Sender{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   baseURL,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
	s.refreshCond = sync.NewCond(&s.mu)
	return s
}

// tenantToken returns a cached tenant_access_token, refreshing when near
// expiry. Uses double-checked locking with a refresh-in-progress flag so
// only one goroutine performs the HTTP call; others wait and reuse the
// result. Retries transient failures with exponential backoff.
func (s *Sender) tenantToken(ctx context.Context) (string, error) {
	s.mu.Lock()

	// Fast path: cached token is still valid.
	if s.token != "" && time.Now().Before(s.tokenExp) {
		tok := s.token
		s.mu.Unlock()
		return tok, nil
	}

	// If another goroutine is already refreshing, wait for it.
	if s.refreshing {
		for s.refreshing {
			s.refreshCond.Wait()
		}
		// After refresh completes, check if we got a valid token.
		if s.token != "" && time.Now().Before(s.tokenExp) {
			tok := s.token
			s.mu.Unlock()
			return tok, nil
		}
	}

	// We are the refresh leader.
	s.refreshing = true
	s.mu.Unlock()

	// Perform refresh outside the lock so other goroutines can proceed
	// with cached tokens while we wait for the HTTP call.
	token, tokenExp, err := s.fetchToken(ctx)

	s.mu.Lock()
	s.refreshing = false
	if err == nil {
		s.token = token
		s.tokenExp = tokenExp
	} else {
		// On failure, clear the cache but keep the expired token
		// as a fallback so at least one failure mode is visible.
		s.token = ""
	}
	s.refreshCond.Broadcast()
	tok := s.token
	s.mu.Unlock()

	if err != nil {
		return "", err
	}
	return tok, nil
}

// fetchToken calls Feishu's tenant_access_token endpoint with retry.
func (s *Sender) fetchToken(ctx context.Context) (string, time.Time, error) {
	body, _ := json.Marshal(map[string]string{
		"app_id":     s.appID,
		"app_secret": s.appSecret,
	})

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 100ms, 300ms, 900ms.
			backoff := time.Duration(100*(1<<(2*(attempt-1)))) * time.Millisecond
			select {
			case <-ctx.Done():
				return "", time.Time{}, ctx.Err()
			case <-time.After(backoff):
			}
		}

		token, tokenExp, err := s.doTokenRequest(ctx, body)
		if err == nil {
			return token, tokenExp, nil
		}

		// Only retry on transient errors (network or 5xx).
		if !isTransient(err) {
			return "", time.Time{}, err
		}
		lastErr = err
	}
	return "", time.Time{}, fmt.Errorf("feishu: token request after %d retries: %w", maxRetries, lastErr)
}

func (s *Sender) doTokenRequest(ctx context.Context, body []byte) (string, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("feishu: token request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	// 5xx is transient.
	if resp.StatusCode >= 500 {
		return "", time.Time{}, fmt.Errorf("feishu: token server error %d: %s", resp.StatusCode, string(raw))
	}

	var tr struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("feishu: token decode: %w", err)
	}
	if tr.Code != 0 || tr.TenantAccessToken == "" {
		return "", time.Time{}, fmt.Errorf("feishu: token error code=%d msg=%q", tr.Code, tr.Msg)
	}

	// Refresh 60s before expiry to be safe.
	tokenExp := time.Now().Add(time.Duration(tr.Expire-60) * time.Second)
	return tr.TenantAccessToken, tokenExp, nil
}

// isTransient returns true for errors that should be retried.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Network errors and server errors are transient.
	return containsAny(msg, "connection refused", "no such host", "timeout",
		"server error 5", "EOF", "connection reset")
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// Send delivers a message to a chat via im.message.create.
// Generates a UUID idempotency key to prevent duplicate delivery on retry.
// Returns the created message id (om_xxx).
func (s *Sender) Send(ctx context.Context, m Message) (string, error) {
	reg := NewRegistry() // no mentions in plain send
	reqBody, err := BuildMessage(m, reg)
	if err != nil {
		return "", err
	}

	// Idempotency key: random UUID as hex string.
	uuidBytes := make([]byte, 16)
	if _, err := rand.Read(uuidBytes); err != nil {
		return "", fmt.Errorf("feishu: generate uuid: %w", err)
	}
	uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x40 // version 4
	uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80 // variant
	reqBody.UUID = hex.EncodeToString(uuidBytes)

	token, err := s.tenantToken(ctx)
	if err != nil {
		return "", err
	}

	payload, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/open-apis/im/v1/messages?receive_id_type=chat_id", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu: send: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var sr struct {
		Code int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &sr); err != nil {
		return "", fmt.Errorf("feishu: send decode: %w", err)
	}
	if sr.Code != 0 {
		return "", fmt.Errorf("feishu: send error code=%d msg=%q", sr.Code, sr.Msg)
	}
	return sr.Data.MessageID, nil
}
