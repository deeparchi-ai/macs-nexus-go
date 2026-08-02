package feishu

import "testing"

func TestPermission_String(t *testing.T) {
	cases := []struct {
		p    Permission
		want string
	}{
		{PermissionForbidden, "forbidden"},
		{PermissionMentionOnly, "mention_only"},
		{PermissionOnDemand, "on_demand"},
		{PermissionAllowed, "allowed"},
		{PermissionOwner, "owner"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Permission(%d).String() = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestRegistry_RegisterAndResolve(t *testing.T) {
	r := NewRegistry()
	r.RegisterBot("ou_deep_001", "cm-deepsight")

	lu, ok := r.ResolveBot("ou_deep_001")
	if !ok || lu != "cm-deepsight" {
		t.Errorf("ResolveBot = %q,%v want cm-deepsight,true", lu, ok)
	}

	if _, ok := r.ResolveBot("ou_human_001"); ok {
		t.Error("human open_id should not resolve to an agent LU")
	}

	openID, ok := r.BotForLU("cm-deepsight")
	if !ok || openID != "ou_deep_001" {
		t.Errorf("BotForLU = %q,%v want ou_deep_001,true", openID, ok)
	}

	if _, ok := r.BotForLU("no-such-agent"); ok {
		t.Error("unknown LU should not resolve to a bot")
	}
}

func TestExtractMentions(t *testing.T) {
	text := `<at user_id="ou_ae4cd929095734e3a69b0b366d09723c">sg</at> 请评审
<at user_id="ou_human_002">张三</at> 你好`
	ids := ExtractMentions(text)
	if len(ids) != 2 {
		t.Fatalf("expected 2 mentions, got %d: %v", len(ids), ids)
	}
	if ids[0] != "ou_ae4cd929095734e3a69b0b366d09723c" {
		t.Errorf("first mention = %q", ids[0])
	}
}

func TestExtractMentions_NoMentions(t *testing.T) {
	if ids := ExtractMentions("plain text no tags"); len(ids) != 0 {
		t.Errorf("expected no mentions, got %v", ids)
	}
}

// Governance matrix from Agent Chat Governance Framework v1.0 (subset):
//
//	             | L0  | L1  | L3 external
//	sg-architect | ALLOWED | ALLOWED | ON_DEMAND
//	cm-deepsight | MENTION_ONLY | ALLOWED | FORBIDDEN
//	cm-success   | MENTION_ONLY | FORBIDDEN | ALLOWED
//	es-reimbursement | FORBIDDEN | FORBIDDEN | FORBIDDEN
func testPolicy() *Policy {
	reg := NewRegistry()
	reg.RegisterBot("ou_sg_001", "sg-architect")
	reg.RegisterBot("ou_deep_001", "cm-deepsight")
	reg.RegisterBot("ou_cs_001", "cm-success")
	reg.RegisterBot("ou_er_001", "es-reimbursement")

	layerOf := func(chatID string) string {
		switch chatID {
		case "oc_l0_all":
			return "L0"
		case "oc_l1_team":
			return "L1"
		case "oc_l3_ext":
			return "L3"
		default:
			return "L3"
		}
	}

	perm := func(lu, layer string) Permission {
		switch lu {
		case "sg-architect":
			if layer == "L3" {
				return PermissionOnDemand
			}
			return PermissionAllowed
		case "cm-deepsight":
			if layer == "L3" {
				return PermissionForbidden
			}
			if layer == "L0" {
				return PermissionMentionOnly
			}
			return PermissionAllowed
		case "cm-success":
			if layer == "L3" {
				return PermissionAllowed
			}
			if layer == "L1" {
				return PermissionForbidden
			}
			return PermissionMentionOnly
		case "es-reimbursement":
			return PermissionForbidden
		default:
			return PermissionForbidden
		}
	}

	return NewPolicy(reg, layerOf, perm)
}

func TestPolicy_AllowedMatrix(t *testing.T) {
	p := testPolicy()

	cases := []struct {
		name      string
		lu        string
		chatID    string
		isMention bool
		want      bool
	}{
		{"sg proactive in L0", "sg-architect", "oc_l0_all", false, true},
		{"sg proactive in L1", "sg-architect", "oc_l1_team", false, true},
		{"deep mention in L0", "cm-deepsight", "oc_l0_all", true, true},
		{"deep silent in L0", "cm-deepsight", "oc_l0_all", false, false},
		{"deep proactive in L1", "cm-deepsight", "oc_l1_team", false, true},
		{"deep forbidden in L3", "cm-deepsight", "oc_l3_ext", true, false},
		{"success allowed in L3", "cm-success", "oc_l3_ext", false, true},
		{"success forbidden in L1", "cm-success", "oc_l1_team", true, false},
		{"er forbidden everywhere", "es-reimbursement", "oc_l0_all", true, false},
		{"sg on-demand in L3 no grant", "sg-architect", "oc_l3_ext", true, false},
	}
	for _, c := range cases {
		got, reason := p.Allowed(c.lu, c.chatID, c.isMention)
		if got != c.want {
			t.Errorf("%s: Allowed = %v (reason %q), want %v", c.name, got, reason, c.want)
		}
	}
}

func TestPolicy_ResolveMentions(t *testing.T) {
	p := testPolicy()

	text := `<at user_id="ou_sg_001">sg</at> <at user_id="ou_human_002">张三</at>`
	lus := p.ResolveMentions(text)
	if len(lus) != 1 || lus[0] != "sg-architect" {
		t.Errorf("ResolveMentions = %v, want [sg-architect]", lus)
	}
}

func TestNewPolicy_NilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on nil registry")
		}
	}()
	NewPolicy(nil, func(string) string { return "L0" }, func(string, string) Permission { return PermissionAllowed })
}
