package feishu

import (
	"bytes"
	"context"
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

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewSender creates a Feishu message sender. baseURL defaults to
// https://open.feishu.cn if empty.
func NewSender(appID, appSecret, baseURL string) *Sender {
	if baseURL == "" {
		baseURL = "https://open.feishu.cn"
	}
	return &Sender{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   baseURL,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

// tenantToken returns a cached tenant_access_token, refreshing when near
// expiry. The token is cached until 60s before expiration.
func (s *Sender) tenantToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && time.Now().Before(s.tokenExp) {
		return s.token, nil
	}

	body, _ := json.Marshal(map[string]string{
		"app_id":     s.appID,
		"app_secret": s.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu: token request: %w", err)
	}
	defer resp.Body.Close()

	var tr struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("feishu: token decode: %w", err)
	}
	if tr.Code != 0 || tr.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu: token error code=%d msg=%q", tr.Code, tr.Msg)
	}

	s.token = tr.TenantAccessToken
	// Refresh 60s before expiry to be safe.
	s.tokenExp = time.Now().Add(time.Duration(tr.Expire-60) * time.Second)
	return s.token, nil
}

// Send delivers a message to a chat via im.message.create.
// Returns the created message id (om_xxx).
func (s *Sender) Send(ctx context.Context, m Message) (string, error) {
	reqBody, err := BuildMessage(m, NewRegistry()) // no mentions in plain send
	if err != nil {
		return "", err
	}

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
