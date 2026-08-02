package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/deeparchi-ai/macs-vtam-go/pkg/feishu"
	"github.com/deeparchi-ai/macs-vtam-go/pkg/vtam"
)

func TestToA2AMessage(t *testing.T) {
	evt := feishu.Event{
		Text:     "帮我查一下",
		ThreadID: "omt_thread_1",
	}
	msg := ToA2AMessage(evt, a2a.MessageRoleUser)
	if msg.ContextID != "omt_thread_1" {
		t.Errorf("ContextID = %q, want omt_thread_1", msg.ContextID)
	}
	if msg.Role != a2a.MessageRoleUser {
		t.Errorf("Role = %q, want user", msg.Role)
	}
	if len(msg.Parts) != 1 {
		t.Fatalf("Parts = %d, want 1", len(msg.Parts))
	}
	if msg.Parts[0].Text() != "帮我查一下" {
		t.Errorf("text part = %q", msg.Parts[0].Text())
	}
}

func TestResultID_Message(t *testing.T) {
	if got := ResultID(&a2a.Message{ID: "msg_1"}); got != "msg_1" {
		t.Errorf("ResultID(message) = %q", got)
	}
}

func TestResultID_Task(t *testing.T) {
	if got := ResultID(&a2a.Task{ID: "task_1"}); got != "task_1" {
		t.Errorf("ResultID(task) = %q", got)
	}
}

func TestResultID_Nil(t *testing.T) {
	if got := ResultID(nil); got != "" {
		t.Errorf("ResultID(nil) = %q, want empty", got)
	}
}

// fakeA2AServer accepts any JSON-RPC request and returns a completed task,
// recording the message text it received.
func fakeA2AServer(t *testing.T, received *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Message struct {
					ContextID string `json:"contextId"`
					Parts     []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("fake server: bad request: %v", err)
			w.WriteHeader(400)
			return
		}
		if received != nil {
			*received = ""
			for _, p := range req.Params.Message.Parts {
				*received += p.Text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "1",
			"result": map[string]any{
				"task": map[string]any{
					"id":     "task_from_server",
					"status": map[string]any{"state": "TASK_STATE_COMPLETED"},
				},
			},
		})
	}))
}

func TestBridge_Send(t *testing.T) {
	srv := fakeA2AServer(t, nil)
	defer srv.Close()

	router := vtam.NewRouter()
	router.Register(vtam.AgentEndpoint{
		LUName:    "cm-deepsight",
		Transport: vtam.TransportA2AHTTP,
		Address:   srv.URL,
	})

	b := New(router)
	evt := feishu.Event{
		Text:     "帮我查一下",
		ThreadID: "omt_thread_1",
	}

	id, err := b.Send(context.Background(), evt, "cm-deepsight")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "task_from_server" {
		t.Errorf("result id = %q, want task_from_server", id)
	}
}

func TestBridge_Send_NoA2ATransport(t *testing.T) {
	router := vtam.NewRouter()
	router.Register(vtam.AgentEndpoint{
		LUName:    "feishu-only-agent",
		Transport: vtam.TransportFeishu,
		Address:   "feishu://ou_xxx",
	})

	b := New(router)
	evt := feishu.Event{Text: "hello"}
	if _, err := b.Send(context.Background(), evt, "feishu-only-agent"); err == nil {
		t.Error("expected error for agent with no A2A transport")
	} else if !strings.Contains(err.Error(), "no A2A transport") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBridge_Send_UnknownLU(t *testing.T) {
	b := New(vtam.NewRouter())
	evt := feishu.Event{Text: "hello"}
	if _, err := b.Send(context.Background(), evt, "no-such-agent"); err == nil {
		t.Error("expected error for unknown LU")
	}
}
