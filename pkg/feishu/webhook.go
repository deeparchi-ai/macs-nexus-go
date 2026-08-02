package feishu

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// WebhookEvent is the Feishu event-subscription payload for
// im.message.receive_v1 (schema 2.0). Only the fields the adapter needs
// are declared; unknown fields are ignored by encoding/json.
type WebhookEvent struct {
	Schema string `json:"schema"`
	Header struct {
		EventID    string `json:"event_id"`
		EventType  string `json:"event_type"`
		CreateTime string `json:"create_time"`
		AppID      string `json:"app_id"`
		Token      string `json:"token"`
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
			SenderType string `json:"sender_type"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			RootID      string `json:"root_id"`
			ParentID    string `json:"parent_id"`
			CreateTime  string `json:"create_time"`
			ChatID      string `json:"chat_id"`
			ChatType    string `json:"chat_type"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"` // JSON-encoded string
		} `json:"message"`
	} `json:"event"`
}

// ParseWebhook decodes a raw Feishu event payload. It returns an error for
// non-receive events and for messages that are not text (the adapter only
// normalizes text messages in this phase).
func ParseWebhook(raw []byte) (*Event, error) {
	var wh WebhookEvent
	if err := json.Unmarshal(raw, &wh); err != nil {
		return nil, fmt.Errorf("feishu: malformed webhook payload: %w", err)
	}
	if wh.Header.EventType != "im.message.receive_v1" {
		return nil, fmt.Errorf("feishu: unsupported event type %q", wh.Header.EventType)
	}
	if wh.Event.Message.MessageType != "text" {
		return nil, fmt.Errorf("feishu: unsupported message type %q (adapter normalizes text only)",
			wh.Event.Message.MessageType)
	}

	evt := &Event{
		EventID:   wh.Header.EventID,
		ChatID:    wh.Event.Message.ChatID,
		Sender:    wh.Event.Sender.SenderID.OpenID,
		MessageID: wh.Event.Message.MessageID,
		ThreadID:  firstNonEmpty(wh.Event.Message.RootID, wh.Event.Message.ParentID),
		ReceivedAt: time.Now(),
	}

	// content is a JSON-encoded string: {"text": "..."}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(wh.Event.Message.Content), &content); err != nil {
		return nil, fmt.Errorf("feishu: malformed message content: %w", err)
	}
	evt.Text = content.Text

	// Sender type "user" means a human; "bot"/"app" means another agent.
	if wh.Event.Sender.SenderType == "bot" || wh.Event.Sender.SenderType == "app" {
		evt.SenderLU = "" // resolved later via Registry when the agent is known
	}

	if ts, err := strconv.ParseInt(wh.Header.CreateTime, 10, 64); err == nil {
		evt.ReceivedAt = time.UnixMilli(ts)
	}

	return evt, nil
}

// ApplyChatLayer sets the governance layer for the event's chat. The layer
// function comes from the deployment's governance matrix (L0–L3).
func (e *Event) ApplyChatLayer(layerOf func(chatID string) string) {
	if e.ChatLayer == "" && layerOf != nil {
		e.ChatLayer = layerOf(e.ChatID)
	}
}

// ResolveSenderLU attempts to map the event sender to an agent LU via the
// registry. Humans stay unresolved (SenderLU remains "").
func (e *Event) ResolveSenderLU(reg *Registry) {
	if reg == nil {
		return
	}
	if lu, ok := reg.ResolveBot(e.Sender); ok {
		e.SenderLU = lu
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
