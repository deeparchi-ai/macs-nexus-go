# macs-vtam-go

MACS §8: Protocol admission control and multi-transport routing for agent networks.

**Status:** v0.5 — 43 tests (vtam 16 + feishu 16 + bridge 7 + nexusd 4)

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

## License

Apache 2.0 — vtam/feishu are zero-dependency (stdlib only); bridge depends
on a2a-go/v2.
