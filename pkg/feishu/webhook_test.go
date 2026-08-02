package feishu

import "testing"

const sampleReceiveV1 = `{
  "schema": "2.0",
  "header": {
    "event_id": "5e7702f883cb6ddfb0d4d3432d7b4c6c",
    "event_type": "im.message.receive_v1",
    "create_time": "1608725989000",
    "token": "t-xyz",
    "app_id": "cli_a97cf099d178dbb3",
    "tenant_key": "tk-abc"
  },
  "event": {
    "sender": {
      "sender_id": {"open_id": "ou_ae4cd929095734e3a69b0b366d09723c"},
      "sender_type": "user",
      "tenant_key": "tk-abc"
    },
    "message": {
      "message_id": "om_5e7702f883cb6ddfb0d4d3432d7b4c6c",
      "root_id": "omt_root_001",
      "parent_id": "",
      "create_time": "1608725989000",
      "chat_id": "oc_l0_all",
      "chat_type": "group",
      "message_type": "text",
      "content": "{\"text\":\"<at user_id=\\\"ou_deep_001\\\">ds</at> 帮我查一下\"}"
    }
  }
}`

func TestParseWebhook_ReceiveV1(t *testing.T) {
	evt, err := ParseWebhook([]byte(sampleReceiveV1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.EventID != "5e7702f883cb6ddfb0d4d3432d7b4c6c" {
		t.Errorf("EventID = %q", evt.EventID)
	}
	if evt.ChatID != "oc_l0_all" {
		t.Errorf("ChatID = %q", evt.ChatID)
	}
	if evt.Sender != "ou_ae4cd929095734e3a69b0b366d09723c" {
		t.Errorf("Sender = %q", evt.Sender)
	}
	if evt.MessageID != "om_5e7702f883cb6ddfb0d4d3432d7b4c6c" {
		t.Errorf("MessageID = %q", evt.MessageID)
	}
	if evt.ThreadID != "omt_root_001" {
		t.Errorf("ThreadID = %q, want omt_root_001 (root_id preferred)", evt.ThreadID)
	}
	if evt.Text == "" || len(evt.Text) < 10 {
		t.Errorf("Text not extracted: %q", evt.Text)
	}
	// ReceivedAt should parse from create_time 1608725989000 ms
	if evt.ReceivedAt.IsZero() {
		t.Error("ReceivedAt should be set from create_time")
	}
}

func TestParseWebhook_WrongEventType(t *testing.T) {
	raw := `{"schema":"2.0","header":{"event_id":"e1","event_type":"im.message.chat_update_v1"},"event":{}}`
	if _, err := ParseWebhook([]byte(raw)); err == nil {
		t.Error("expected error for non-receive event type")
	}
}

func TestParseWebhook_NonTextMessage(t *testing.T) {
	raw := `{"schema":"2.0","header":{"event_id":"e1","event_type":"im.message.receive_v1"},
		"event":{"sender":{"sender_id":{"open_id":"ou_x"},"sender_type":"user"},
		"message":{"message_id":"om_x","chat_id":"oc_x","message_type":"image","content":"{\"image_key\":\"img_v2\"}"}}}`
	if _, err := ParseWebhook([]byte(raw)); err == nil {
		t.Error("expected error for non-text message type")
	}
}

func TestParseWebhook_Malformed(t *testing.T) {
	if _, err := ParseWebhook([]byte(`{not json`)); err == nil {
		t.Error("expected error for malformed payload")
	}
}

func TestEvent_ApplyChatLayer(t *testing.T) {
	evt := &Event{ChatID: "oc_l3_ext"}
	evt.ApplyChatLayer(func(chatID string) string {
		if chatID == "oc_l3_ext" {
			return "L3"
		}
		return "L0"
	})
	if evt.ChatLayer != "L3" {
		t.Errorf("ChatLayer = %q, want L3", evt.ChatLayer)
	}
}

func TestEvent_ResolveSenderLU(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterBot("ou_ae4cd929095734e3a69b0b366d09723c", "sg-architect")

	evt := &Event{Sender: "ou_ae4cd929095734e3a69b0b366d09723c"}
	evt.ResolveSenderLU(reg)
	if evt.SenderLU != "sg-architect" {
		t.Errorf("SenderLU = %q, want sg-architect", evt.SenderLU)
	}

	human := &Event{Sender: "ou_human_002"}
	human.ResolveSenderLU(reg)
	if human.SenderLU != "" {
		t.Errorf("human SenderLU = %q, want empty", human.SenderLU)
	}
}
