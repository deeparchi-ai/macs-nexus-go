// Command e2e is a live-connection smoke test for the Nexus Feishu adapter:
// it builds an outbound message with a real bot open_id and prints the
// resulting request body so it can be verified against the live Feishu API.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/deeparchi-ai/macs-vtam-go/pkg/feishu"
)

func main() {
	botOpenID := os.Getenv("FEISHU_BOT_OPEN_ID")
	if botOpenID == "" {
		fmt.Fprintln(os.Stderr, "FEISHU_BOT_OPEN_ID required")
		os.Exit(1)
	}

	reg := feishu.NewRegistry()
	reg.RegisterBot(botOpenID, "hermes-home")

	req, err := feishu.BuildMessage(feishu.Message{
		ChatID:   os.Getenv("FEISHU_CHAT_ID"),
		Text:     "Nexus Feishu adapter 实连测试 — 这条消息由 macs-nexus-go BuildMessage 生成",
		Mentions: []string{"hermes-home"},
	}, reg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	out, _ := json.MarshalIndent(req, "", "  ")
	fmt.Println(string(out))

	// Parse back the content to show the human-readable form.
	var content struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(req.Content), &content)
	fmt.Println("\n--- decoded content ---")
	fmt.Println(content.Text)
}
