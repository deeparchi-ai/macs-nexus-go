package feishu

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// maxTimestampDelta is the maximum allowed clock skew between the Feishu
// server and this adapter (5 minutes). Requests with timestamps outside
// this window are rejected as replay attacks.
const maxTimestampDelta = 5 * time.Minute

// VerifySignature validates a Feishu webhook request's integrity.
//
// Parameters match Feishu's event-subscription signature mechanism:
//
//	timestamp  — X-Lark-Request-Timestamp header (Unix seconds)
//	nonce      — X-Lark-Request-Nonce header (random string)
//	signature  — X-Lark-Signature header (base64 HMAC-SHA256)
//	signingKey — the "Verification Token" from the Feishu app console
//
// Returns nil if the signature is valid and the timestamp is within the
// replay window. Returns an error describing the failure otherwise.
func VerifySignature(timestamp, nonce, signature, signingKey string) error {
	if timestamp == "" || nonce == "" || signature == "" {
		return fmt.Errorf("feishu verify: missing required header(s)")
	}
	if signingKey == "" {
		return fmt.Errorf("feishu verify: signing key not configured")
	}

	// Replay protection: reject if timestamp is outside the window.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("feishu verify: invalid timestamp %q: %w", timestamp, err)
	}
	reqTime := time.Unix(ts, 0)
	delta := time.Since(reqTime)
	if delta < 0 {
		delta = -delta
	}
	if delta > maxTimestampDelta {
		return fmt.Errorf("feishu verify: timestamp outside replay window (delta=%v, max=%v)", delta, maxTimestampDelta)
	}

	// HMAC-SHA256(timestamp + nonce + signing_key)
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(timestamp))
	mac.Write([]byte(nonce))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("feishu verify: signature mismatch")
	}
	return nil
}

// URLChallenge is the Feishu event-subscription URL verification payload.
// When you first configure a webhook URL, Feishu sends a POST with this
// JSON body. The server must respond with the {challenge} field within 1s.
type URLChallenge struct {
	Challenge string `json:"challenge"`
	Token     string `json:"token"`
	Type      string `json:"type"`
}

// IsURLChallenge returns true when the raw body is a Feishu URL
// verification challenge (rather than an event delivery).
func IsURLChallenge(raw []byte) bool {
	var ch URLChallenge
	if err := json.Unmarshal(raw, &ch); err != nil {
		return false
	}
	return ch.Type == "url_verification" && ch.Challenge != ""
}

// ChallengeResponse returns the JSON response body for a URL verification
// challenge: {"challenge": "<value>"}.
func ChallengeResponse(challenge string) string {
	return fmt.Sprintf(`{"challenge":%q}`, challenge)
}
