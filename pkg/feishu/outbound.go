package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OutboundRequest is the Feishu im.message.create request body (schema 1.0
// message payloads). The adapter builds this from a Message; the actual HTTP
// call is left to the deployment layer (lark-cli or Feishu SDK) — the
// adapter stays dependency-free and thin.
type OutboundRequest struct {
	ReceiveID  string `json:"receive_id"`
	MsgType    string `json:"msg_type"`
	Content    string `json:"content"` // JSON-encoded string
	UUID       string `json:"uuid,omitempty"`
}

// BuildMessage converts a Message into an im.message.create request body.
// Mentions (agent LU names) are expanded to Feishu <at> tags using the
// registry. ReplyTo is not part of this body — reply uses a different
// endpoint (im.message.reply), exposed separately.
func BuildMessage(m Message, reg *Registry) (*OutboundRequest, error) {
	if m.ChatID == "" {
		return nil, fmt.Errorf("feishu: message requires ChatID")
	}

	text := m.Text
	for _, lu := range m.Mentions {
		openID, ok := reg.BotForLU(lu)
		if !ok {
			continue // unknown LU: skip mention, keep text as-is
		}
		// Only insert <at> if not already mentioned by the caller.
		if !strings.Contains(text, `<at user_id="`+openID+`">`) {
			text += fmt.Sprintf(`<at user_id="%s">%s</at>`, openID, lu)
		}
	}

	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, fmt.Errorf("feishu: marshal content: %w", err)
	}

	return &OutboundRequest{
		ReceiveID: m.ChatID,
		MsgType:   "text",
		Content:   string(content),
	}, nil
}

// BuildReply converts a Message into an im.message.reply request body.
// The reply endpoint takes {content} and optionally {msg_type}.
func BuildReply(m Message, reg *Registry) (map[string]string, error) {
	text := m.Text
	for _, lu := range m.Mentions {
		openID, ok := reg.BotForLU(lu)
		if !ok {
			continue
		}
		if !strings.Contains(text, `<at user_id="`+openID+`">`) {
			text += fmt.Sprintf(`<at user_id="%s">%s</at>`, openID, lu)
		}
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, fmt.Errorf("feishu: marshal content: %w", err)
	}
	return map[string]string{
		"content":  string(content),
		"msg_type": "text",
	}, nil
}
