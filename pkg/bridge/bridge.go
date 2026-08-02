// Package bridge connects the Feishu transport to the A2A protocol:
// a normalized Feishu event becomes an A2A message sent to the target
// agent's best transport (resolved by the Nexus router).
//
// The package is deliberately thin: it depends on a2a-go's client for the
// protocol (dependency > implementation) and on pkg/vtam for routing.
// pkg/feishu and pkg/vtam themselves stay dependency-free.
package bridge

import (
	"context"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/deeparchi-ai/macs-vtam-go/pkg/feishu"
	"github.com/deeparchi-ai/macs-vtam-go/pkg/vtam"
)

// Bridge routes Feishu events to remote agents over A2A.
type Bridge struct {
	router *vtam.Router
}

// New creates a Bridge backed by a Nexus router. The router resolves a
// target agent's LU name to its best transport.
func New(router *vtam.Router) *Bridge {
	return &Bridge{router: router}
}

// ToA2AMessage converts a normalized Feishu event into an A2A message.
// The Feishu thread id becomes the A2A context id so multi-turn
// conversations keep their thread continuity.
func ToA2AMessage(evt feishu.Event, role a2a.MessageRole) *a2a.Message {
	msg := a2a.NewMessage(role, a2a.NewTextPart(evt.Text))
	msg.ContextID = evt.ThreadID
	return msg
}

// Send delivers the event text to a target agent via its best A2A transport.
// It resolves the target LU through the router, builds a client from the
// endpoint, and sends a user-role message carrying the Feishu thread
// context. Returns the A2A message id on success.
func (b *Bridge) Send(ctx context.Context, evt feishu.Event, target vtam.LUName) (string, error) {
	ep, reason, err := b.router.Route("feishu-adapter", target, nil)
	if err != nil {
		return "", fmt.Errorf("bridge: route %q: %w", target, err)
	}
	if ep.Transport != vtam.TransportA2AHTTP && ep.Transport != vtam.TransportA2AgRPC {
		return "", fmt.Errorf("bridge: target %q has no A2A transport (selected %s: %s)", target, ep.Transport, reason)
	}

	// Build the client from the endpoint's address. The transport protocol
	// follows from the Nexus transport: HTTP/gRPC map to A2A bindings.
	binding := a2a.TransportProtocolJSONRPC
	if ep.Transport == vtam.TransportA2AgRPC {
		binding = a2a.TransportProtocolGRPC
	}
	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
		{URL: ep.Address, ProtocolBinding: binding, ProtocolVersion: a2a.Version},
	})
	if err != nil {
		return "", fmt.Errorf("bridge: build A2A client for %q: %w", target, err)
	}
	defer client.Destroy()

	req := &a2a.SendMessageRequest{
		Message: ToA2AMessage(evt, a2a.MessageRoleUser),
	}
	result, err := client.SendMessage(ctx, req)
	if err != nil {
		return "", fmt.Errorf("bridge: send to %q: %w", target, err)
	}

	return ResultID(result), nil
}

// ResultID extracts a stable identifier from an A2A send result.
// Message results carry a message id; task results carry a task id.
func ResultID(result a2a.SendMessageResult) string {
	switch r := result.(type) {
	case *a2a.Message:
		return r.ID
	case *a2a.Task:
		return string(r.ID)
	default:
		return ""
	}
}
