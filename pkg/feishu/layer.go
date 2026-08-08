// Package feishu — Governance layer types and standardised L0–L3 policy.
//
// Layer semantics are modelled after the Agent Chat Governance Framework v1.0:
//
//	L0  — 完全隔离：仅记录，不路由（bootstrap / supervisor only）
//	L1  — 白名单受限：白名单 agent + 审计
//	L2  — 受限路由：受限路由 + 审计
//	L3  — 默认放行：公开渠道 / 对外群
//
// Every layer is fail-closed by default: unknown LU or unmatched layer
// always returns PermissionForbidden.
package feishu

import (
	"fmt"
)

// Layer is a governance layer identifier.
type Layer string

const (
	LayerL0 Layer = "L0" // 完全隔离
	LayerL1 Layer = "L1" // 白名单受限
	LayerL2 Layer = "L2" // 受限路由
	LayerL3 Layer = "L3" // 默认放行
)

// Valid returns true for recognised layer identifiers.
func (l Layer) Valid() bool {
	switch l {
	case LayerL0, LayerL1, LayerL2, LayerL3:
		return true
	}
	return false
}

// LayerBehavior describes the default governance behaviour for a layer.
type LayerBehavior struct {
	// Description is a human-readable summary of the layer's purpose.
	Description string
	// DefaultPermission is the fallback when no explicit LU rule matches.
	DefaultPermission Permission
	// AllowMentionOverride permits @mention to elevate permission
	// when no explicit LU override exists in this layer.
	AllowMentionOverride bool
	// AuditEnabled indicates that decisions in this layer should be logged.
	AuditEnabled bool
}

// DefaultLayerBehaviors returns the standard L0–L3 behaviours.
// Every layer is fail-closed: unknown LU → Forbidden.
func DefaultLayerBehaviors() map[Layer]LayerBehavior {
	return map[Layer]LayerBehavior{
		LayerL0: {
			Description:          "完全隔离 — 仅记录不路由，bootstrap/supervisor only",
			DefaultPermission:    PermissionForbidden,
			AllowMentionOverride: false,
			AuditEnabled:         true,
		},
		LayerL1: {
			Description:          "白名单受限 — 白名单 agent + 审计",
			DefaultPermission:    PermissionForbidden,
			AllowMentionOverride: true,
			AuditEnabled:         true,
		},
		LayerL2: {
			Description:          "受限路由 — 受限路由 + 审计",
			DefaultPermission:    PermissionForbidden,
			AllowMentionOverride: true,
			AuditEnabled:         true,
		},
		LayerL3: {
			Description:          "默认放行 — 公开渠道 / 对外群",
			DefaultPermission:    PermissionAllowed,
			AllowMentionOverride: true,
			AuditEnabled:         false,
		},
	}
}

// Decision is a structured audit record of a policy check.
type Decision struct {
	Allowed    bool       // whether the LU may respond
	Reason     string     // human-readable reason
	Layer      Layer      // resolved governance layer
	LU         string     // agent LU name
	Permission Permission // effective permission
	IsMention  bool       // whether the agent was @mentioned
}

// String returns a compact audit-line representation:
//
//	[allow] lu=cm-deepsight layer=L1 perm=mention_only mention=true reason=mentioned
func (d Decision) String() string {
	status := "deny"
	if d.Allowed {
		status = "allow"
	}
	return fmt.Sprintf("[%s] lu=%s layer=%s perm=%s mention=%t reason=%s",
		status, d.LU, d.Layer, d.Permission, d.IsMention, d.Reason)
}

// LayerPolicy is a standardised governance policy with built-in L0–L3
// behaviours and a fail-closed default. Per-LU permissions override the
// layer defaults.
type LayerPolicy struct {
	registry  *Registry
	layerOf   func(chatID string) Layer
	behaviors map[Layer]LayerBehavior
	luPerms   map[string]map[Layer]Permission // LU → layer → perm override
}

// LayerOption configures a LayerPolicy.
type LayerOption func(*LayerPolicy)

// WithBehaviors replaces the default layer behaviours.
func WithBehaviors(b map[Layer]LayerBehavior) LayerOption {
	return func(lp *LayerPolicy) {
		lp.behaviors = b
	}
}

// WithLUPermissions registers per-LU overrides for the governance matrix.
// The map shape is lu → layer name → permission string (e.g. "allowed").
// Permissions not listed fall through to the layer's DefaultPermission.
func WithLUPermissions(perms map[string]map[string]Permission) LayerOption {
	return func(lp *LayerPolicy) {
		for lu, layerPerms := range perms {
			if lp.luPerms[lu] == nil {
				lp.luPerms[lu] = make(map[Layer]Permission)
			}
			for layerName, perm := range layerPerms {
				lp.luPerms[lu][Layer(layerName)] = perm
			}
		}
	}
}

// NewLayerPolicy creates a standardised policy with default L0–L3 behaviours.
// layerOf maps a chat ID to a governance layer. registry is the bot identity
// registry. Options can override behaviours or register per-LU permissions.
//
// If layerOf returns an unknown layer, the policy treats it as fail-closed
// (Forbidden). If a chat ID is not mapped, LayerL3 is the fallback.
func NewLayerPolicy(registry *Registry, layerOf func(chatID string) Layer, opts ...LayerOption) *LayerPolicy {
	if registry == nil {
		panic("feishu: NewLayerPolicy requires a non-nil registry")
	}
	if layerOf == nil {
		panic("feishu: NewLayerPolicy requires a non-nil layerOf")
	}

	lp := &LayerPolicy{
		registry:  registry,
		layerOf:   layerOf,
		behaviors: DefaultLayerBehaviors(),
		luPerms:   make(map[string]map[Layer]Permission),
	}
	for _, opt := range opts {
		opt(lp)
	}
	return lp
}

// behaviorFor returns the layer behaviour, falling back to fail-closed.
func (lp *LayerPolicy) behaviorFor(layer Layer) LayerBehavior {
	if b, ok := lp.behaviors[layer]; ok {
		return b
	}
	// Fail-closed: unknown layers are treated as fully isolated.
	return LayerBehavior{
		Description:          fmt.Sprintf("unknown layer %q — fail-closed", layer),
		DefaultPermission:    PermissionForbidden,
		AllowMentionOverride: false,
		AuditEnabled:         true,
	}
}

// resolvePermission returns the effective permission and whether it came
// from an explicit LU override (true) or the layer default (false).
func (lp *LayerPolicy) resolvePermission(lu string, layer Layer) (Permission, bool) {
	if layerPerms, ok := lp.luPerms[lu]; ok {
		if perm, ok := layerPerms[layer]; ok {
			return perm, true
		}
	}
	return lp.behaviorFor(layer).DefaultPermission, false
}

// Check evaluates whether an agent LU may respond in a chat, returning a
// structured Decision suitable for audit logging.
func (lp *LayerPolicy) Check(lu, chatID string, isMention bool) Decision {
	layer := lp.resolveLayer(chatID)
	behavior := lp.behaviorFor(layer)
	perm, fromOverride := lp.resolvePermission(lu, layer)

	d := Decision{
		Layer:      layer,
		LU:         lu,
		Permission: perm,
		IsMention:  isMention,
	}

	switch perm {
	case PermissionAllowed, PermissionOwner:
		d.Allowed = true
		d.Reason = fmt.Sprintf("layer=%s perm=%s", layer, perm)
	case PermissionMentionOnly:
		// An explicit LU override always permits mention elevation.
		// When coming from the layer default, the layer's
		// AllowMentionOverride flag determines the outcome.
		mentionOK := fromOverride || behavior.AllowMentionOverride
		if isMention && mentionOK {
			d.Allowed = true
			d.Reason = fmt.Sprintf("layer=%s perm=mention_only (mentioned)", layer)
		} else {
			d.Allowed = false
			if isMention && !mentionOK {
				d.Reason = fmt.Sprintf("layer=%s perm=mention_only, mention blocked by layer", layer)
			} else {
				d.Reason = fmt.Sprintf("layer=%s perm=mention_only, not mentioned", layer)
			}
		}
	case PermissionOnDemand:
		d.Allowed = false
		d.Reason = fmt.Sprintf("layer=%s perm=on_demand, no explicit grant", layer)
	case PermissionForbidden:
		d.Allowed = false
		d.Reason = fmt.Sprintf("layer=%s perm=forbidden", layer)
	default:
		d.Allowed = false
		d.Permission = PermissionForbidden
		d.Reason = fmt.Sprintf("layer=%s perm=unknown, fail-closed", layer)
	}

	return d
}

// Allowed is the backward-compatible boolean check. It delegates to Check
// and returns (bool, string).
func (lp *LayerPolicy) Allowed(lu, chatID string, isMention bool) (bool, string) {
	d := lp.Check(lu, chatID, isMention)
	return d.Allowed, d.Reason
}

// resolveLayer returns the governance layer for a chat ID, falling back to
// LayerL3 when no mapping exists (or when the mapping returns empty).
func (lp *LayerPolicy) resolveLayer(chatID string) Layer {
	if lp.layerOf == nil {
		return LayerL3
	}
	l := lp.layerOf(chatID)
	if l == "" {
		return LayerL3
	}
	return l
}

// Registry returns the identity registry backing the policy.
func (lp *LayerPolicy) Registry() *Registry {
	return lp.registry
}

// ResolveMentions converts raw mention open_ids to agent LU names using the
// registry. Unknown ids (humans, other bots) are dropped.
func (lp *LayerPolicy) ResolveMentions(rawText string) []string {
	ids := ExtractMentions(rawText)
	lus := make([]string, 0, len(ids))
	for _, id := range ids {
		if lu, ok := lp.registry.ResolveBot(id); ok {
			lus = append(lus, lu)
		}
	}
	return lus
}
