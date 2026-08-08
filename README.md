# macs-vtam-go

MACS §8: Protocol admission control and multi-transport routing for agent networks.

**Status:** v0.8 — 120 tests (vtam 16 + feishu 50 + bridge 11 + nexusd 8, 35 sub-tests)

## What

Ports z/OS VTAM (1974) Logical Unit naming to agent protocols:
- **LU Names**: Stable agent identifiers independent of transport (A2A, MCP, Feishu)
- **Transport resolution**: Automatic best-transport selection (gRPC > HTTP > WebSocket)
- **Admission control**: Per-agent-pair rules with method allowlists, time windows, rate limits
- **Circuit observability**: Connection-level event log for debugging and auditing
- **Health tracking**: Mark/unhealthy endpoints with automatic failover

## Usage

```go
import "github.com/deeparchi-ai/macs-vtam-go/pkg/vtam"

r := vtam.NewRouter()
r.Register(vtam.AgentEndpoint{
    LUName: "research-agent", Transport: vtam.TransportA2AgRPC,
    Address: "grpc://research:50051",
})

// Route selects the best available transport
ep, reason, err := r.Route("source", "research-agent", nil)

// Admission check
r.AddRule(vtam.AdmissionRule{
    SourceAgent: "source", TargetAgent: "research-agent",
    AllowedMethods: []string{"tasks/send"},
})
decision := r.CheckAdmission("source", "research-agent", "tasks/send")
```

## Feishu adapter

The `pkg/feishu` package implements the Feishu transport: event
normalization, @mention → LU resolution, and the L0–L3 policy gate at the
message boundary. Design spec: `macs/specs/nexus-feishu-adapter.md`.

```go
import "github.com/deeparchi-ai/macs-vtam-go/pkg/feishu"

reg := feishu.NewRegistry()
reg.RegisterBot("ou_deep_001", "cm-deepsight")

// Policy encodes the governance matrix (layer resolution + permissions)
p := feishu.NewPolicy(reg, layerOf, perm)
allowed, reason := p.Allowed("cm-deepsight", "oc_l0_all", isMention=true)

// Resolve @mentions in raw Feishu text to agent LU names
lus := p.ResolveMentions(`<at user_id="ou_deep_001">ds</at> 帮我查一下`)
```

## A2A bridge

The `pkg/bridge` package routes Feishu events to remote agents over the
A2A protocol. It resolves the target agent's best transport via the Nexus
router, then sends the event text as an A2A message (thread id → context id
for multi-turn continuity). Depends on `a2a-go/v2`; feishu and vtam stay
dependency-free.

```go
import "github.com/deeparchi-ai/macs-vtam-go/pkg/bridge"

b := bridge.New(router)
taskID, err := b.Send(ctx, evt, "cm-deepsight")
```

## nexusd — Feishu-native MACS deployment skeleton

`cmd/nexusd` assembles the Nexus packages into a runnable service: a Feishu
webhook endpoint that normalizes events, applies the L0–L3 policy gate, and
routes @mentions to target agents via A2A. All deployment-specific wiring
(bot registry, chat layers, permission matrix, agent endpoints) lives in
`config.yaml` — the core packages are untouched. This is the "Feishu version
of QM" control plane.

```bash
go run ./cmd/nexusd -config cmd/nexusd/config.example.yaml
# POST /feishu/webhook  (Feishu event subscription callback)
# GET  /healthz
```

With `feishu_app_id` / `feishu_app_secret` configured and `reply_on_route:
true`, the service also sends a routed confirmation back to the source chat
via `pkg/feishu.Sender` (tenant token → im.message.create).
Live-verified 2026-08-03: webhook → policy → A2A → Feishu reply round-trip.

## v0.8 Governance Layer Standardization

See `docs/governance-layers-v0.8.md` for design. New in v0.8:

- **Layer type** (`pkg/feishu/layer.go`) — L0–L3 as first-class types with standardised semantics
- **LayerPolicy** — built-in default behaviours per layer with fail-closed matrix
- **Decision audit** — `Check()` returns structured `Decision` with layer, permission, mention state for audit logging
- **Backward compat** — `NewPolicy`/`Allowed` signature unchanged; `Policy` gains `Check()` method
- **Table-driven tests** — 30-case matrix covering all 4 layers × mention × authorised
- **Default behaviours**: L0=完全隔离, L1=白名单受限, L2=受限路由, L3=默认放行

## v0.7 Security Hardening

See `docs/security-hardening-v0.7.md` for design. New in v0.7:

- **Feishu webhook HMAC signature verification** (`pkg/feishu/verify.go`) — X-Lark-Signature validation + 5-min replay window
- **Tenant token concurrency hardening** (`pkg/feishu/sender.go`) — double-checked locking, exponential backoff retry
- **Outbound idempotency** (`pkg/feishu/sender.go`) — random UUID per send to prevent duplicate messages
- **Bridge input validation** (`pkg/bridge/bridge.go`) — empty target/text rejection, 128KB text truncation

## License

Apache 2.0 — vtam/feishu are zero-dependency (stdlib only); bridge depends
on a2a-go/v2.
