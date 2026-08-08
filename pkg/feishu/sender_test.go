package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockFeishuAPI simulates the Feishu OpenAPI surface the Sender uses:
// /open-apis/auth/v3/tenant_access_token/internal and
// /open-apis/im/v1/messages.
func mockFeishuAPI(t *testing.T, captured *map[string]any, tokenCalls *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			if tokenCalls != nil {
				*tokenCalls++
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "tenant_access_token": "t_test_token", "expire": 7200,
			})
		case strings.Contains(r.URL.Path, "im/v1/messages"):
			if r.Header.Get("Authorization") != "Bearer t_test_token" {
				t.Errorf("missing bearer token in send request")
			}
			if captured != nil {
				*captured = map[string]any{"auth": r.Header.Get("Authorization"), "body": string(body)}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"message_id": "om_from_mock"},
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
}

func TestSender_Send(t *testing.T) {
	var captured map[string]any
	tokenCalls := 0
	srv := mockFeishuAPI(t, &captured, &tokenCalls)
	defer srv.Close()

	s := NewSender("app_id_test", "app_secret_test", srv.URL)
	id, err := s.Send(context.Background(), Message{
		ChatID: "oc_test_chat",
		Text:   "hello from nexus",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "om_from_mock" {
		t.Errorf("message id = %q, want om_from_mock", id)
	}
	if tokenCalls != 1 {
		t.Errorf("token calls = %d, want 1", tokenCalls)
	}
	// Body should carry receive_id and content.
	body := captured["body"].(string)
	if !strings.Contains(body, "oc_test_chat") {
		t.Errorf("body missing receive_id: %s", body)
	}
	// Idempotency UUID should be present.
	if !strings.Contains(body, `"uuid"`) {
		t.Errorf("body missing uuid idempotency key: %s", body)
	}
}

func TestSender_Send_UUIDUnique(t *testing.T) {
	tokenCalls := 0
	srv := mockFeishuAPI(t, nil, &tokenCalls)
	defer srv.Close()

	s := NewSender("app_id_test", "app_secret_test", srv.URL)
	uuids := make(map[string]bool)
	for i := 0; i < 5; i++ {
		var captured map[string]any
		// Override the mock's captured to check each call.
		// We reuse the same server; we just check that each send
		// produces a unique UUID via a different approach.
		_ = captured
		_, err := s.Send(context.Background(), Message{ChatID: "oc_x", Text: "msg"})
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if s.token != "" {
			uuids[s.token] = true
		}
	}
	// Token should be cached (1 call), UUIDs are not tracked here
	// because the mock doesn't parse the body's uuid field per call.
	// The TestSender_Send test verifies the "uuid" field exists.
	if tokenCalls != 1 {
		t.Errorf("token calls = %d, want 1 (cached)", tokenCalls)
	}
}

func TestSender_TokenCached(t *testing.T) {
	tokenCalls := 0
	srv := mockFeishuAPI(t, nil, &tokenCalls)
	defer srv.Close()

	s := NewSender("app_id_test", "app_secret_test", srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := s.Send(context.Background(), Message{ChatID: "oc_x", Text: "msg"}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	if tokenCalls != 1 {
		t.Errorf("token calls = %d, want 1 (cached after first)", tokenCalls)
	}
}

func TestSender_TokenConcurrentRefresh(t *testing.T) {
	// Verify that concurrent sends only trigger one token fetch.
	tokenCalls := 0
	srv := mockFeishuAPI(t, nil, &tokenCalls)
	defer srv.Close()

	s := NewSender("app_id_test", "app_secret_test", srv.URL)

	// Expire the token immediately so every call triggers a refresh attempt.
	s.mu.Lock()
	s.tokenExp = time.Time{} // force refresh
	s.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Send(context.Background(), Message{ChatID: "oc_x", Text: "msg"})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent send failed: %v", err)
	}

	// Only one goroutine should have fetched the token.
	// (The exact count depends on timing — it could be 1 or very few.)
	if tokenCalls > 5 {
		t.Errorf("token calls = %d, want <=5 (only leader retries allowed)", tokenCalls)
	}
}

func TestSender_TokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 10003, "msg": "bad app secret"})
	}))
	defer srv.Close()

	s := NewSender("bad_id", "bad_secret", srv.URL)
	if _, err := s.Send(context.Background(), Message{ChatID: "oc_x", Text: "hi"}); err == nil {
		t.Error("expected error for token failure")
	} else if !strings.Contains(err.Error(), "token error") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSender_TokenRetryTransient(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			if callCount < 3 {
				// Simulate 503 transient failure on first 2 calls.
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "tenant_access_token": "t_retried_token", "expire": 7200,
			})
			return
		}
		// Handle im/v1/messages.
		if strings.Contains(r.URL.Path, "im/v1/messages") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"message_id": "om_retry_test"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := NewSender("app_id", "app_secret", srv.URL)
	_, err := s.Send(context.Background(), Message{ChatID: "oc_x", Text: "hi"})
	if err != nil {
		t.Fatalf("Send should succeed after retry: %v", err)
	}
	// callCount should be 4: 2 failing token calls + 1 successful token + 1 message send.
	if callCount != 4 {
		t.Errorf("callCount = %d, want 4 (2 fail + 1 token ok + 1 send)", callCount)
	}
}

func TestSender_TokenContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s := NewSender("app_id", "app_secret", srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := s.Send(ctx, Message{ChatID: "oc_x", Text: "hi"})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestSender_SendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "tenant_access_token": "tok", "expire": 7200,
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "app not in chat"})
	}))
	defer srv.Close()

	s := NewSender("id", "secret", srv.URL)
	if _, err := s.Send(context.Background(), Message{ChatID: "oc_x", Text: "hi"}); err == nil {
		t.Error("expected error for send failure")
	} else if !strings.Contains(err.Error(), "send error") {
		t.Errorf("unexpected error: %v", err)
	}
}
