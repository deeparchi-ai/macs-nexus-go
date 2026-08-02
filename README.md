# macs-vtam-go

MACS §8: Protocol admission control and multi-transport routing for agent networks.

**Status:** v0.3 — 32 tests (vtam 16 + feishu 16)

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

## License

Apache 2.0 — zero dependencies (stdlib only).
