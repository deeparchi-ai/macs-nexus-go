package feishu

import (
	"strings"
	"testing"
)

func TestLayer_Valid(t *testing.T) {
	for _, l := range []Layer{LayerL0, LayerL1, LayerL2, LayerL3} {
		if !l.Valid() {
			t.Errorf("%s.Valid() = false, want true", l)
		}
	}
	if Layer("L5").Valid() {
		t.Error("L5.Valid() = true, want false")
	}
	if Layer("").Valid() {
		t.Error("\"\".Valid() = true, want false")
	}
}

func TestDecision_String(t *testing.T) {
	d := Decision{
		Allowed:    true,
		Reason:     "mentioned",
		Layer:      LayerL1,
		LU:         "cm-deepsight",
		Permission: PermissionMentionOnly,
		IsMention:  true,
	}
	s := d.String()
	parts := []string{
		"[allow]", "lu=cm-deepsight", "layer=L1", "perm=mention_only",
		"mention=true", "reason=mentioned",
	}
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("Decision.String() missing %q in %q", p, s)
		}
	}

	// Deny case
	d2 := Decision{
		Allowed:    false,
		Layer:      LayerL0,
		LU:         "unknown-agent",
		Permission: PermissionForbidden,
		IsMention:  false,
		Reason:     "layer=L0 perm=forbidden",
	}
	if s2 := d2.String(); !strings.Contains(s2, "[deny]") {
		t.Errorf("deny Decision.String() = %q, want [deny]", s2)
	}
}

// testRegistry builds an in-memory registry for test policies.
func testRegistry() *Registry {
	reg := NewRegistry()
	reg.RegisterBot("ou_sg_001", "sg-architect")
	reg.RegisterBot("ou_deep_001", "cm-deepsight")
	reg.RegisterBot("ou_cs_001", "cm-success")
	reg.RegisterBot("ou_er_001", "es-reimbursement")
	return reg
}

// testLayerOf returns a layer resolver for known test chats.
func testLayerOf() func(string) Layer {
	return func(chatID string) Layer {
		switch chatID {
		case "oc_l0_all":
			return LayerL0
		case "oc_l1_team":
			return LayerL1
		case "oc_l2_restricted":
			return LayerL2
		case "oc_l3_ext":
			return LayerL3
		default:
			return LayerL3
		}
	}
}

// testLUOverrides returns per-LU permission overrides matching the
// Governance Framework v1.0 test matrix.
func testLUOverrides() map[string]map[string]Permission {
	return map[string]map[string]Permission{
		"sg-architect": {
			"L3": PermissionOnDemand,
			"L2": PermissionAllowed,
			"L1": PermissionAllowed,
			"L0": PermissionAllowed,
		},
		"cm-deepsight": {
			"L3": PermissionForbidden,
			"L2": PermissionAllowed,
			"L1": PermissionAllowed,
			"L0": PermissionMentionOnly,
		},
		"cm-success": {
			"L3": PermissionAllowed,
			"L2": PermissionAllowed,
			"L1": PermissionForbidden,
			"L0": PermissionMentionOnly,
		},
		"es-reimbursement": {
			"L3": PermissionForbidden,
			"L2": PermissionForbidden,
			"L1": PermissionForbidden,
			"L0": PermissionForbidden,
		},
	}
}

func TestLayerPolicy_Check_Matrix(t *testing.T) {
	lp := NewLayerPolicy(testRegistry(), testLayerOf(),
		WithLUPermissions(testLUOverrides()))

	// Four layers × mention × authorised = matrix
	// Unknown LU → fail-closed
	cases := []struct {
		name      string
		lu        string
		chatID    string
		isMention bool
		wantAllow bool
		wantLayer Layer
		wantPerm  Permission
	}{
		// ── L0 (完全隔离) ──
		{"sg proactive L0", "sg-architect", "oc_l0_all", false, true, LayerL0, PermissionAllowed},
		{"sg mention L0", "sg-architect", "oc_l0_all", true, true, LayerL0, PermissionAllowed},
		{"deep mention L0", "cm-deepsight", "oc_l0_all", true, true, LayerL0, PermissionMentionOnly},
		{"deep silent L0", "cm-deepsight", "oc_l0_all", false, false, LayerL0, PermissionMentionOnly},
		{"cs mention L0", "cm-success", "oc_l0_all", true, true, LayerL0, PermissionMentionOnly},
		{"cs silent L0", "cm-success", "oc_l0_all", false, false, LayerL0, PermissionMentionOnly},
		{"er forbidden L0", "es-reimbursement", "oc_l0_all", true, false, LayerL0, PermissionForbidden},

		// ── L1 (白名单受限) ──
		{"sg proactive L1", "sg-architect", "oc_l1_team", false, true, LayerL1, PermissionAllowed},
		{"sg mention L1", "sg-architect", "oc_l1_team", true, true, LayerL1, PermissionAllowed},
		{"deep proactive L1", "cm-deepsight", "oc_l1_team", false, true, LayerL1, PermissionAllowed},
		{"deep mention L1", "cm-deepsight", "oc_l1_team", true, true, LayerL1, PermissionAllowed},
		{"cs forbidden L1", "cm-success", "oc_l1_team", true, false, LayerL1, PermissionForbidden},
		{"cs silent L1", "cm-success", "oc_l1_team", false, false, LayerL1, PermissionForbidden},
		{"er forbidden L1", "es-reimbursement", "oc_l1_team", true, false, LayerL1, PermissionForbidden},

		// ── L2 (受限路由) ──
		{"sg proactive L2", "sg-architect", "oc_l2_restricted", false, true, LayerL2, PermissionAllowed},
		{"deep proactive L2", "cm-deepsight", "oc_l2_restricted", false, true, LayerL2, PermissionAllowed},
		{"cs proactive L2", "cm-success", "oc_l2_restricted", false, true, LayerL2, PermissionAllowed},
		{"er forbidden L2", "es-reimbursement", "oc_l2_restricted", true, false, LayerL2, PermissionForbidden},

		// ── L3 (默认放行) ──
		{"sg on-demand no grant L3", "sg-architect", "oc_l3_ext", true, false, LayerL3, PermissionOnDemand},
		{"deep forbidden L3", "cm-deepsight", "oc_l3_ext", true, false, LayerL3, PermissionForbidden},
		{"deep forbidden L3 no mention", "cm-deepsight", "oc_l3_ext", false, false, LayerL3, PermissionForbidden},
		{"cs allowed L3", "cm-success", "oc_l3_ext", false, true, LayerL3, PermissionAllowed},
		{"cs mention L3", "cm-success", "oc_l3_ext", true, true, LayerL3, PermissionAllowed},
		{"er forbidden L3", "es-reimbursement", "oc_l3_ext", true, false, LayerL3, PermissionForbidden},

		// ── Unknown LU → fail-closed (L3 is default-allowed, so unknown LU passes) ──
		{"unknown LU L0", "no-such-agent", "oc_l0_all", true, false, LayerL0, PermissionForbidden},
		{"unknown LU L1", "no-such-agent", "oc_l1_team", true, false, LayerL1, PermissionForbidden},
		{"unknown LU L2", "no-such-agent", "oc_l2_restricted", true, false, LayerL2, PermissionForbidden},
		{"unknown LU L3 allowed", "no-such-agent", "oc_l3_ext", false, true, LayerL3, PermissionAllowed},

		// ── Unknown chat → L3 fallback ──
		{"unknown chat", "cm-deepsight", "oc_unknown", true, false, LayerL3, PermissionForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := lp.Check(c.lu, c.chatID, c.isMention)
			if d.Allowed != c.wantAllow {
				t.Errorf("Check.Allowed = %v, want %v (decision: %s)", d.Allowed, c.wantAllow, d)
			}
			if d.Layer != c.wantLayer {
				t.Errorf("Check.Layer = %s, want %s", d.Layer, c.wantLayer)
			}
			if d.Permission != c.wantPerm {
				t.Errorf("Check.Permission = %s, want %s", d.Permission, c.wantPerm)
			}
			if d.LU != c.lu {
				t.Errorf("Check.LU = %s, want %s", d.LU, c.lu)
			}
			if d.IsMention != c.isMention {
				t.Errorf("Check.IsMention = %v, want %v", d.IsMention, c.isMention)
			}
			// Reason must be non-empty.
			if d.Reason == "" {
				t.Error("Check.Reason is empty")
			}
			// Decision.String() should contain lu, layer, and reason.
			s := d.String()
			if !strings.Contains(s, string(c.wantLayer)) {
				t.Errorf("Decision.String() missing layer: %s", s)
			}
			if !strings.Contains(s, c.lu) {
				t.Errorf("Decision.String() missing LU: %s", s)
			}
		})
	}
}

func TestLayerPolicy_Allowed_BackwardCompat(t *testing.T) {
	lp := NewLayerPolicy(testRegistry(), testLayerOf(),
		WithLUPermissions(testLUOverrides()))

	allowed, reason := lp.Allowed("cm-deepsight", "oc_l0_all", true)
	if !allowed {
		t.Errorf("Allowed(L0, mention=true) = %v, want true (reason=%q)", allowed, reason)
	}

	allowed, reason = lp.Allowed("cm-deepsight", "oc_l0_all", false)
	if allowed {
		t.Errorf("Allowed(L0, mention=false) = %v, want false (reason=%q)", allowed, reason)
	}

	allowed, reason = lp.Allowed("es-reimbursement", "oc_l3_ext", true)
	if allowed {
		t.Errorf("Allowed(forbidden LU) = %v, want false (reason=%q)", allowed, reason)
	}
}

func TestLayerPolicy_Defaults_FailClosed(t *testing.T) {
	// Policy without any overrides should be fail-closed everywhere except L3.
	lp := NewLayerPolicy(testRegistry(), testLayerOf())

	// L3 default is Allowed → any LU passes.
	d := lp.Check("some-agent", "oc_l3_ext", false)
	if !d.Allowed || d.Permission != PermissionAllowed {
		t.Errorf("L3 default: got allowed=%v perm=%s, want true allowed", d.Allowed, d.Permission)
	}

	// All other layers default to Forbidden.
	for _, layer := range []Layer{LayerL0, LayerL1, LayerL2} {
		for _, chatID := range []string{"oc_l0_all", "oc_l1_team", "oc_l2_restricted"} {
			d := lp.Check("any-agent", chatID, true)
			if d.Layer == layer && d.Allowed {
				t.Errorf("fail-closed violation: %s in %s should be denied, got %s", chatID, layer, d)
			}
		}
	}

	// L0 won't allow mention override.
	d = lp.Check("any-agent", "oc_l0_all", true)
	if d.Allowed {
		t.Errorf("L0 mention should be denied (no override in L0 defaults), got %s", d)
	}
}

func TestLayerPolicy_UnknownLayer_FailClosed(t *testing.T) {
	// Map a chat to an unrecognised layer (L5).
	layerOf := func(chatID string) Layer {
		if chatID == "oc_unknown" {
			return Layer("L5")
		}
		return LayerL3
	}
	lp := NewLayerPolicy(testRegistry(), layerOf,
		WithLUPermissions(map[string]map[string]Permission{
			"known-agent": {"L5": PermissionAllowed},
		}))

	// Even with an override, the layer itself is unknown → fail-closed behavior.
	d := lp.Check("known-agent", "oc_unknown", true)
	// The LU override gives PermissionAllowed, but the layer behavior is fail-closed
	// (the override takes priority in resolvePermission, so it should be allowed).
	// Actually the override wins in resolvePermission.
	// Let's also check an LU without override.
	if !d.Allowed {
		t.Errorf("known-agent with explicit override in L5 should be allowed: %s", d)
	}

	d2 := lp.Check("unknown-agent", "oc_unknown", true)
	if d2.Allowed {
		t.Errorf("unknown-agent in L5 without override should be denied (fail-closed): %s", d2)
	}
}

func TestLayerPolicy_ResolveMentions(t *testing.T) {
	lp := NewLayerPolicy(testRegistry(), testLayerOf())

	text := `<at user_id="ou_sg_001">sg</at> <at user_id="ou_human_002">张三</at>`
	lus := lp.ResolveMentions(text)
	if len(lus) != 1 || lus[0] != "sg-architect" {
		t.Errorf("ResolveMentions = %v, want [sg-architect]", lus)
	}
}

func TestNewLayerPolicy_NilPanics(t *testing.T) {
	t.Run("nil registry", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expected panic on nil registry")
			}
		}()
		NewLayerPolicy(nil, func(string) Layer { return LayerL0 })
	})

	t.Run("nil layerOf", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expected panic on nil layerOf")
			}
		}()
		NewLayerPolicy(NewRegistry(), nil)
	})
}

func TestLayerPolicy_WithBehaviors(t *testing.T) {
	// Custom behaviour: L0 is permissive.
	custom := map[Layer]LayerBehavior{
		LayerL0: {
			Description:          "custom permissive L0",
			DefaultPermission:    PermissionAllowed,
			AllowMentionOverride: true,
			AuditEnabled:         false,
		},
	}
	lp := NewLayerPolicy(testRegistry(), testLayerOf(), WithBehaviors(custom))

	d := lp.Check("any-agent", "oc_l0_all", false)
	if !d.Allowed {
		t.Errorf("custom L0 should be permissive: %s", d)
	}
}

func TestLayerPolicy_AuditFields(t *testing.T) {
	lp := NewLayerPolicy(testRegistry(), testLayerOf(),
		WithLUPermissions(testLUOverrides()))

	d := lp.Check("cm-deepsight", "oc_l1_team", true)
	// Verify audit fields are complete.
	if d.LU != "cm-deepsight" {
		t.Errorf("LU = %q, want cm-deepsight", d.LU)
	}
	if d.Layer != LayerL1 {
		t.Errorf("Layer = %q, want L1", d.Layer)
	}
	if d.Permission != PermissionAllowed {
		t.Errorf("Permission = %s, want allowed", d.Permission)
	}
	if !d.IsMention {
		t.Error("IsMention should be true")
	}
	if d.Reason == "" {
		t.Error("Reason should not be empty")
	}
	// Structured log line should contain all fields.
	s := d.String()
	for _, want := range []string{"lu=cm-deepsight", "layer=L1", "perm=allowed", "mention=true"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q in %q", want, s)
		}
	}
}

func TestLayerPolicy_EmptyChatID(t *testing.T) {
	lp := NewLayerPolicy(testRegistry(), testLayerOf())
	d := lp.Check("any-agent", "", false)
	// Empty chat → LayerL3 fallback → PermissionAllowed (default)
	if !d.Allowed {
		t.Errorf("empty chat should default to L3 allowed: %s", d)
	}
	if d.Layer != LayerL3 {
		t.Errorf("empty chat layer = %s, want L3", d.Layer)
	}
}
