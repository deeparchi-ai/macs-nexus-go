package feishu

import (
	"strings"
	"testing"
)

func TestBuildMessage_Basic(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterBot("ou_deep_001", "cm-deepsight")

	req, err := BuildMessage(Message{
		ChatID:   "oc_l0_all",
		Text:     "收到，正在处理",
		Mentions: []string{"cm-deepsight"},
	}, reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.ReceiveID != "oc_l0_all" {
		t.Errorf("ReceiveID = %q", req.ReceiveID)
	}
	if req.MsgType != "text" {
		t.Errorf("MsgType = %q", req.MsgType)
	}
	if !strings.Contains(req.Content, `\u003cat user_id=\"ou_deep_001\"\u003e`) {
		t.Errorf("content missing mention (JSON-escaped form): %s", req.Content)
	}
	// UUID is not set by BuildMessage — it's set by Sender.Send.
	if req.UUID != "" {
		t.Logf("UUID set by BuildMessage: %s (expected empty, set by Sender)", req.UUID)
	}
}

func TestBuildMessage_NoDoubleMention(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterBot("ou_deep_001", "cm-deepsight")

	req, err := BuildMessage(Message{
		ChatID:   "oc_l0_all",
		Text:     `<at user_id="ou_deep_001">cm-deepsight</at> 请查`,
		Mentions: []string{"cm-deepsight"},
	}, reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The <at> tag appears exactly once (not appended twice)
	if count := strings.Count(req.Content, "ou_deep_001"); count != 1 {
		t.Errorf("mention duplicated: content=%s (count=%d)", req.Content, count)
	}
}

func TestBuildMessage_UnknownLU(t *testing.T) {
	reg := NewRegistry()

	req, err := BuildMessage(Message{
		ChatID:   "oc_l0_all",
		Text:     "hello",
		Mentions: []string{"no-such-agent"},
	}, reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(req.Content, "<at") {
		t.Errorf("unknown LU should not produce mention: %s", req.Content)
	}
}

func TestBuildMessage_EmptyChatID(t *testing.T) {
	reg := NewRegistry()
	if _, err := BuildMessage(Message{Text: "hi"}, reg); err == nil {
		t.Error("expected error for empty ChatID")
	}
}

func TestBuildReply(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterBot("ou_sg_001", "sg-architect")

	body, err := BuildReply(Message{
		ChatID:   "oc_l1_team",
		Text:     "评审通过",
		Mentions: []string{"sg-architect"},
	}, reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["msg_type"] != "text" {
		t.Errorf("msg_type = %q", body["msg_type"])
	}
	if !strings.Contains(body["content"], `\u003cat user_id=\"ou_sg_001\"\u003e`) {
		t.Errorf("reply content missing mention (JSON-escaped form): %s", body["content"])
	}
}
