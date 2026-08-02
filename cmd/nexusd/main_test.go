package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deeparchi-ai/macs-vtam-go/pkg/feishu"
	"github.com/deeparchi-ai/macs-vtam-go/pkg/vtam"
)

// sampleConfig mirrors config.example.yaml but with a test A2A endpoint.
func sampleConfig() *Config {
	return &Config{
		ListenAddr:  ":0",
		DefaultLayer: "L3",
		Bots: map[string]string{
			"ou_sg_001": "sg-architect",
			"ou_hermes": "hermes-home",
		},
		Layers: map[string]string{
			"oc_l1_team": "L1",
			"oc_l0_all":  "L0",
		},
		Permissions: map[string]map[string]string{
			"sg-architect": {
				"L0": "allowed",
				"L1": "allowed",
			},
			"hermes-home": {
				"L0": "mention_only",
				"L1": "mention_only",
			},
		},
		Endpoints: []EndpointConfig{},
	}
}

// webhookPayload builds a Feishu im.message.receive_v1 payload.
func webhookPayload(chatID, sender, text string) []byte {
	content, _ := json.Marshal(map[string]string{"text": text})
	payload := map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":    "evt_1",
			"event_type":  "im.message.receive_v1",
			"create_time": "1608725989000",
			"app_id":      "cli_test",
		},
		"event": map[string]any{
			"sender": map[string]any{
				"sender_id": map[string]any{"open_id": sender},
				"sender_type": "user",
			},
			"message": map[string]any{
				"message_id":   "om_test",
				"chat_id":      chatID,
				"message_type": "text",
				"content":      string(content),
			},
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func TestHandleWebhook_RoutesMention(t *testing.T) {
	// A2A target that records the message.
	var received string
	a2aSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = "hit"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "1",
			"result": map[string]any{
				"task": map[string]any{
					"id":     "task_1",
					"status": map[string]any{"state": "TASK_STATE_COMPLETED"},
				},
			},
		})
	}))
	defer a2aSrv.Close()

	cfg := sampleConfig()
	cfg.Endpoints = []EndpointConfig{
		{LU: "sg-architect", Transport: "a2a-http", Address: a2aSrv.URL},
	}

	_, policy, router, layerOf := buildWiring(cfg)
	b := newTestBridge(router)
	srv := &server{policy: policy, bridge: b, layerOf: layerOf, log: testLogger()}

	// sg is ALLOWED in L1 → mention routes to the A2A endpoint.
	body := webhookPayload("oc_l1_team", "ou_human_1",
		`<at user_id="ou_sg_001">sg</at> 帮我评审`)
	req := httptest.NewRequest(http.MethodPost, "/feishu/webhook", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.handleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if received != "hit" {
		t.Error("A2A endpoint was not hit — mention not routed")
	}
}

func TestHandleWebhook_DeniedByPolicy(t *testing.T) {
	cfg := sampleConfig()
	_, policy, router, layerOf := buildWiring(cfg)
	b := newTestBridge(router)
	srv := &server{policy: policy, bridge: b, layerOf: layerOf, log: testLogger()}

	// hermes-home is FORBIDDEN in L3 (default) → mention must NOT route.
	body := webhookPayload("oc_unlisted_chat", "ou_human_1",
		`<at user_id="ou_hermes">h</at> hello`)
	req := httptest.NewRequest(http.MethodPost, "/feishu/webhook", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.handleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (ack even for denied)", rec.Code)
	}
}

func TestHandleWebhook_NonMessageEvent(t *testing.T) {
	cfg := sampleConfig()
	_, policy, router, layerOf := buildWiring(cfg)
	srv := &server{policy: policy, bridge: newTestBridge(router), layerOf: layerOf, log: testLogger()}

	raw := `{"schema":"2.0","header":{"event_id":"e2","event_type":"im.message.chat_update_v1"}}`
	req := httptest.NewRequest(http.MethodPost, "/feishu/webhook", strings.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.handleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("non-message event should be ack'd 200, got %d", rec.Code)
	}
}

func TestHandleWebhook_NoMentions(t *testing.T) {
	cfg := sampleConfig()
	_, policy, router, layerOf := buildWiring(cfg)
	srv := &server{policy: policy, bridge: newTestBridge(router), layerOf: layerOf, log: testLogger()}

	// Plain message with no @mention — nothing to route, still ack.
	body := webhookPayload("oc_l0_all", "ou_human_1", "早上好")
	req := httptest.NewRequest(http.MethodPost, "/feishu/webhook", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.handleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("no-mention message should be ack'd 200, got %d", rec.Code)
	}
}

// testBridge builds a bridge whose router has no endpoints (fail-closed
// for route errors — the handler logs and continues).
func newTestBridge(router *vtam.Router) *testBridgeWrapper {
	return &testBridgeWrapper{router: router}
}

type testBridgeWrapper struct {
	router *vtam.Router
}

func (w *testBridgeWrapper) Send(ctx context.Context, evt feishu.Event, target vtam.LUName) (string, error) {
	ep, _, err := w.router.Route("feishu-adapter", target, nil)
	if err != nil {
		return "", err
	}
	// Minimal HTTP round-trip to the endpoint.
	resp, err := http.Post(ep.Address, "application/json", strings.NewReader(`{}`))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return "task_from_test", nil
}

// testLogger silences test log output.
func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}
