# MACS Nexus v0.7 — Security Hardening Design

**Version:** 0.7  
**Date:** 2026-08-08  
**Scope:** Feishu transport + A2A bridge security hardening

## Gap Analysis

### GAP F1: Feishu Webhook Event Signature Verification (CRITICAL)

**Current state:** `cmd/nexusd/main.go` `handleWebhook` accepts any POST body as a valid Feishu event. No signature verification, no replay protection. An attacker who knows the webhook endpoint URL can inject arbitrary events.

**Feishu signing mechanism:**
- HTTP headers: `X-Lark-Request-Timestamp` (Unix seconds), `X-Lark-Request-Nonce` (random string), `X-Lark-Signature` (base64 HMAC-SHA256)
- Signature = Base64(HMAC-SHA256(timestamp + nonce + signing_key))
- Signing key = "Verification Token" from Feishu app console → Event Subscriptions

**Fix:**
1. New package `pkg/feishu/verify.go`: `VerifySignature(body, timestamp, nonce, signature, signingKey)` → error
2. Replay protection: reject if `|now - timestamp| > 5 min`
3. Wire into `cmd/nexusd/main.go` before `ParseWebhook`
4. New config fields: `feishu_signing_key`

**Test plan:** Table-driven with known HMAC vectors (compute expected signature from known key + timestamp + nonce), test timestamp outside window, test wrong key, test missing headers.

### GAP F2: Tenant Token Concurrency & Robustness (HIGH)

**Current state:** `sender.go` `tenantToken()` holds `s.mu.Lock()` during the HTTP call to Feishu's token endpoint. This serializes ALL callers during refresh, causing head-of-line blocking under concurrency. No retry on transient failures.

**Fix:**
1. Double-checked locking pattern: only one goroutine performs the HTTP refresh; others wait on a sync.Cond or channel
2. Use `sync.Mutex` for cache access only; separate `refreshMu` for the HTTP call
3. Exponential backoff retry on transient failures (3 retries: 100ms, 300ms, 900ms)
4. Return context errors promptly (respect `ctx.Done()`)

**Test plan:** Concurrent Send calls (goroutines) verify only 1 token fetch, retry on 503→success, cancelled context propagation.

### GAP F3: Outbound Idempotency (MEDIUM)

**Current state:** `sender.go` `Send()` sends `im.message.create` without a UUID idempotency key. Feishu supports the `uuid` field in request body. Network retries can produce duplicate messages.

**Fix:**
1. Generate a random UUID (stdlib `crypto/rand` → hex) per Send call
2. Pass it through `OutboundRequest.UUID` (field already exists but unused)

**Test plan:** Verify UUID present in request body, unique across calls.

### GAP F4: Bridge Input Validation (MEDIUM)

**Current state:** `bridge.go` `Send()` passes `evt.Text` and `target` directly to A2A without validation. Empty target LU, empty text, and extremely long text are unhandled.

**Fix:**
1. Reject empty `target` LUName
2. Reject empty `evt.Text`
3. Truncate text to 128KB (prevents memory exhaustion in downstream agents)
4. Validate source adapter name in Route call

**Test plan:** Table-driven: empty target, empty text, oversized text.

---

## Implementation Plan

| Gap | File(s) | New/Mod | Lines (est) |
|-----|---------|---------|-------------|
| F1 | `pkg/feishu/verify.go` | New | ~40 |
| F1 | `pkg/feishu/verify_test.go` | New | ~70 |
| F1 | `cmd/nexusd/main.go` | Mod | +20 |
| F1 | `cmd/nexusd/main_test.go` | Mod | +30 |
| F2 | `pkg/feishu/sender.go` | Mod | +50 |
| F2 | `pkg/feishu/sender_test.go` | Mod | +60 |
| F3 | `pkg/feishu/sender.go` | Mod | +10 |
| F3 | `pkg/feishu/outbound.go` | Mod | +5 |
| F3 | `pkg/feishu/outbound_test.go` | Mod | +20 |
| F4 | `pkg/bridge/bridge.go` | Mod | +25 |
| F4 | `pkg/bridge/bridge_test.go` | Mod | +40 |
| — | `README.md` | Mod | +2 |

## Test Matrix

| Test Case | Package | Gap |
|-----------|---------|-----|
| VerifySignature valid | feishu | F1 |
| VerifySignature wrong key | feishu | F1 |
| VerifySignature timestamp expired | feishu | F1 |
| VerifySignature missing headers | feishu | F1 |
| URL challenge (verification token) | feishu | F1 |
| Token concurrent refresh (1 fetch) | feishu | F2 |
| Token retry on 5xx | feishu | F2 |
| Token context cancelled | feishu | F2 |
| Send with UUID idempotency | feishu | F3 |
| BuildMessage includes UUID | feishu | F3 |
| Bridge empty target | bridge | F4 |
| Bridge empty text | bridge | F4 |
| Bridge oversized text | bridge | F4 |
| Webhook signature rejected 401 | nexusd | F1 |
| Webhook signature ok 200 | nexusd | F1 |

**Estimated new tests:** ~15  
**Total after:** 47 + 15 = 62
