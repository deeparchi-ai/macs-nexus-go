// Package feishu implements the Nexus Feishu transport: event
// normalization, @mention → LU resolution, and the L0–L3 policy gate
// at the message boundary.
//
// z/OS VTAM (1974) let applications talk to Logical Unit names instead
// of wires. The Feishu adapter extends that: a Feishu bot open_id is
// just another way to reach the same agent, with Feishu as the transport.
package feishu

import (
	"regexp"
	"sync"
	"time"
)

// Permission levels from the Agent Chat Governance Framework v1.0.
type Permission int

const (
	// PermissionForbidden — never allowed to speak in this layer.
	PermissionForbidden Permission = iota
	// PermissionMentionOnly — replies only when @mentioned.
	PermissionMentionOnly
	// PermissionOnDemand — allowed only after explicit grant.
	PermissionOnDemand
	// PermissionAllowed — may speak proactively.
	PermissionAllowed
	// PermissionOwner — DM hub, always allowed.
	PermissionOwner
)

// String returns the permission name.
func (p Permission) String() string {
	switch p {
	case PermissionForbidden:
		return "forbidden"
	case PermissionMentionOnly:
		return "mention_only"
	case PermissionOnDemand:
		return "on_demand"
	case PermissionAllowed:
		return "allowed"
	case PermissionOwner:
		return "owner"
	default:
		return "unknown"
	}
}

// Event is a normalized Feishu event, independent of the Feishu API shape.
type Event struct {
	EventID     string       // dedup key
	ChatID      string       // oc_xxx
	ChatLayer   string       // "L0".."L3" — from governance framework
	Sender      string       // sender open_id
	SenderLU    string       // resolved agent LU ("" if human)
	MessageID   string       // om_xxx
	ThreadID    string       // omt_xxx (optional)
	Text        string       // plain text content
	Mentions    []string     // @mentioned agents, resolved to LU names
	Attachments []Attachment // files/images in the message
	ReceivedAt  time.Time    // when the adapter saw it
}

// Attachment describes a file/image in a Feishu message.
type Attachment struct {
	Kind    string // "image" | "file" | "audio" | "video"
	FileKey string // Feishu file_key, downloadable via API
}

// Message is an outgoing Feishu message.
type Message struct {
	ChatID   string   // oc_xxx
	ReplyTo  string   // om_xxx — reply to a specific message (thread support)
	Text     string   // markdown/text content
	Mentions []string // agent LU names, resolved to open_ids before send
	Files    []string // local paths, uploaded via im:resource
}

// Registry maps Feishu bot identities to agent LU names (1:1, no fuzzy match).
type Registry struct {
	mu    sync.RWMutex
	botLU map[string]string // bot open_id -> agent LU
	luBot map[string]string // agent LU -> bot open_id (for egress mentions)
}

// NewRegistry creates an empty identity registry.
func NewRegistry() *Registry {
	return &Registry{
		botLU: make(map[string]string),
		luBot: make(map[string]string),
	}
}

// RegisterBot binds a Feishu bot open_id to an agent LU name.
func (r *Registry) RegisterBot(openID, lu string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.botLU[openID] = lu
	r.luBot[lu] = openID
}

// ResolveBot maps a mentioned open_id to an agent LU. Returns false when the
// open_id is not a known agent bot (i.e. a human or an unknown entity).
func (r *Registry) ResolveBot(openID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lu, ok := r.botLU[openID]
	return lu, ok
}

// BotForLU returns the Feishu open_id that represents an agent LU, for
// constructing outbound @mentions. Returns false when the LU has no bot.
func (r *Registry) BotForLU(lu string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	openID, ok := r.luBot[lu]
	return openID, ok
}

// atPattern matches Feishu's mention XML: <at user_id="ou_xxx">name</at>.
var atPattern = regexp.MustCompile(`<at user_id="([^"]+)"`)

// ExtractMentions returns the open_ids from <at> tags in raw Feishu text.
func ExtractMentions(text string) []string {
	matches := atPattern.FindAllStringSubmatch(text, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// Policy encodes the governance matrix at the message boundary.
// Chat layers (L0–L3) and per-agent permissions come from the
// Agent Chat Governance Framework v1.0.
type Policy struct {
	registry *Registry
	// layerOf returns the governance layer ("L0".."L3") for a chat id.
	layerOf func(chatID string) string
	// perm returns the permission of an agent LU in a layer.
	perm func(lu, layer string) Permission
}

// NewPolicy builds a Policy. layerOf and perm must be supplied by the
// deployment (they encode the governance matrix); nil panics.
func NewPolicy(registry *Registry, layerOf func(chatID string) string, perm func(lu, layer string) Permission) *Policy {
	if registry == nil || layerOf == nil || perm == nil {
		panic("feishu: NewPolicy requires registry, layerOf, perm")
	}
	return &Policy{registry: registry, layerOf: layerOf, perm: perm}
}

// Allowed checks whether agent LU may respond in a chat, and why.
// isMention is true when the agent was explicitly @mentioned.
//
// Deprecated: use Check() for structured audit decisions.
func (p *Policy) Allowed(lu, chatID string, isMention bool) (bool, string) {
	d := p.Check(lu, chatID, isMention)
	return d.Allowed, d.Reason
}

// Check evaluates whether an agent LU may respond in a chat, returning a
// structured Decision suitable for audit logging.
func (p *Policy) Check(lu, chatID string, isMention bool) Decision {
	layer := p.layerOf(chatID)
	perm := p.perm(lu, layer)

	d := Decision{
		Layer:      Layer(layer),
		LU:         lu,
		Permission: perm,
		IsMention:  isMention,
	}

	switch perm {
	case PermissionAllowed, PermissionOwner:
		d.Allowed = true
		d.Reason = "allowed"
	case PermissionMentionOnly:
		if isMention {
			d.Allowed = true
			d.Reason = "mentioned"
		} else {
			d.Allowed = false
			d.Reason = "mention_only: not mentioned"
		}
	case PermissionOnDemand:
		d.Allowed = false
		d.Reason = "on_demand: no explicit grant"
	case PermissionForbidden:
		d.Allowed = false
		d.Reason = "forbidden in layer " + layer
	default:
		d.Allowed = false
		d.Reason = "unknown permission"
	}

	return d
}

// Registry returns the identity registry backing the policy.
func (p *Policy) Registry() *Registry {
	return p.registry
}

// ResolveMentions converts raw mention open_ids to agent LU names using the
// registry. Unknown ids (humans, other bots) are dropped.
func (p *Policy) ResolveMentions(rawText string) []string {
	ids := ExtractMentions(rawText)
	lus := make([]string, 0, len(ids))
	for _, id := range ids {
		if lu, ok := p.registry.ResolveBot(id); ok {
			lus = append(lus, lu)
		}
	}
	return lus
}
