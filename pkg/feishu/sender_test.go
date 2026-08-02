package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	// Body should carry receive_id and content
	body := captured["body"].(string)
	if !strings.Contains(body, "oc_test_chat") {
		t.Errorf("body missing receive_id: %s", body)
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
