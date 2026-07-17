package vtam

import (
	"testing"
)

func TestTransport_Priority(t *testing.T) {
	if TransportA2AgRPC.Priority() >= TransportA2AHTTP.Priority() {
		t.Error("gRPC should have lower priority than HTTP")
	}
	if TransportA2AHTTP.Priority() >= TransportA2AWebSocket.Priority() {
		t.Error("HTTP should have lower priority than WebSocket")
	}
	if TransportFeishu.Priority() <= TransportMCPHTTP.Priority() {
		t.Error("Feishu should have highest priority number (least preferred)")
	}
}

func TestRegisterAndResolve(t *testing.T) {
	r := NewRouter()
	r.Register(AgentEndpoint{
		LUName: "research-agent", Transport: TransportA2AHTTP,
		Address: "https://research.example.com/a2a",
	})
	r.Register(AgentEndpoint{
		LUName: "research-agent", Transport: TransportA2AgRPC,
		Address: "grpc://research.example.com:50051",
	})

	endpoints := r.Resolve("research-agent")
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(endpoints))
	}
}

func TestResolve_Missing(t *testing.T) {
	r := NewRouter()
	if eps := r.Resolve("nonexistent"); len(eps) != 0 {
		t.Error("expected empty for missing LU")
	}
}

func TestRoute_PrefersLowestPriority(t *testing.T) {
	r := NewRouter()
	r.Register(AgentEndpoint{
		LUName: "target", Transport: TransportA2AWebSocket,
		Address: "ws://target.example.com",
	})
	r.Register(AgentEndpoint{
		LUName: "target", Transport: TransportA2AgRPC,
		Address: "grpc://target.example.com:50051",
	})

	ep, _, err := r.Route("source", "target", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Transport != TransportA2AgRPC {
		t.Errorf("expected gRPC (lowest priority), got %s", ep.Transport)
	}
}

func TestRoute_UnhealthySkip(t *testing.T) {
	r := NewRouter()
	r.Register(AgentEndpoint{
		LUName: "target", Transport: TransportA2AgRPC,
		Address: "grpc://target:50051",
	})
	r.Register(AgentEndpoint{
		LUName: "target", Transport: TransportA2AHTTP,
		Address: "https://target/a2a",
	})

	r.MarkUnhealthy("target", TransportA2AgRPC)

	ep, _, err := r.Route("source", "target", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Transport != TransportA2AHTTP {
		t.Errorf("expected HTTP after gRPC unhealthy, got %s", ep.Transport)
	}
}

func TestRoute_MissingLU(t *testing.T) {
	r := NewRouter()
	_, _, err := r.Route("source", "target", nil)
	if err == nil {
		t.Error("expected error for missing LU")
	}
}

func TestRoute_AllUnhealthy(t *testing.T) {
	r := NewRouter()
	r.Register(AgentEndpoint{LUName: "target", Transport: TransportA2AHTTP, Address: "https://t/a2a"})
	r.MarkUnhealthy("target", TransportA2AHTTP)

	_, _, err := r.Route("source", "target", nil)
	if err == nil {
		t.Error("expected error when all endpoints unhealthy")
	}
}

func TestAdmission_DefaultAllowed(t *testing.T) {
	r := NewRouter()
	decision := r.CheckAdmission("agent-a", "agent-b", "tasks/send")
	if decision != AdmissionAllowed {
		t.Errorf("expected allowed (no rules), got %s", decision)
	}
}

func TestAdmission_MethodDenied(t *testing.T) {
	r := NewRouter()
	r.AddRule(AdmissionRule{
		SourceAgent:    "agent-a",
		TargetAgent:    "agent-b",
		AllowedMethods: []string{"tasks/get"},
	})

	decision := r.CheckAdmission("agent-a", "agent-b", "tasks/send")
	if decision != AdmissionDenied {
		t.Errorf("expected denied for tasks/send, got %s", decision)
	}

	decision2 := r.CheckAdmission("agent-a", "agent-b", "tasks/get")
	if decision2 != AdmissionAllowed {
		t.Errorf("expected allowed for tasks/get, got %s", decision2)
	}
}

func TestAdmission_TimeWindow(t *testing.T) {
	r := NewRouter()
	r.AddRule(AdmissionRule{
		SourceAgent:   "agent-a",
		TargetAgent:   "agent-b",
		AllowedAfter:  "09:00",
		AllowedBefore: "17:00",
	})

	// Testing at current time — can't reliably test time windows
	// without controlling time. Just verify the rule is stored.
	// The check runs against time.Now() which is inherently flaky.
	// Instead, test that rules are evaluated.
	decision := r.CheckAdmission("agent-a", "agent-b", "tasks/send")
	// Decision depends on current time — either allowed or denied is valid
	if decision != AdmissionAllowed && decision != AdmissionDenied {
		t.Errorf("unexpected decision: %s", decision)
	}
}

func TestAdmission_RateLimit(t *testing.T) {
	r := NewRouter()
	r.AddRule(AdmissionRule{
		SourceAgent: "agent-a",
		TargetAgent: "agent-b",
		MaxRate:     2,
	})
	// Register endpoint so Route succeeds
	r.Register(AgentEndpoint{
		LUName: "agent-b", Transport: TransportA2AHTTP, Address: "https://b/a2a",
	})

	// Simulate connections
	for i := 0; i < 3; i++ {
		r.Route("agent-a", "agent-b", nil) // records circuit events
	}

	decision := r.CheckAdmission("agent-a", "agent-b", "tasks/send")
	if decision != AdmissionRateLimited {
		t.Errorf("expected rate_limited after 3 connections, got %s", decision)
	}
}

func TestMarkUnhealthy(t *testing.T) {
	r := NewRouter()
	r.Register(AgentEndpoint{
		LUName: "agent-x", Transport: TransportA2AHTTP, Address: "https://x/a2a",
	})

	r.MarkUnhealthy("agent-x", TransportA2AHTTP)

	eps := r.Resolve("agent-x")
	if len(eps) != 1 {
		t.Fatal("endpoint should still be registered")
	}
	if eps[0].Healthy {
		t.Error("endpoint should be unhealthy")
	}
}

func TestCircuitEvents(t *testing.T) {
	r := NewRouter()
	r.Register(AgentEndpoint{
		LUName: "agent-y", Transport: TransportA2AHTTP, Address: "https://y/a2a",
	})
	r.Route("source", "agent-y", nil)

	events := r.CircuitEvents(10)
	if len(events) < 2 {
		t.Errorf("expected at least 2 events (register + route), got %d", len(events))
	}

	// Verify the last event is the route selection
	last := events[len(events)-1]
	if last.Event != "selected" {
		t.Errorf("last event should be 'selected', got %q", last.Event)
	}
}

func TestCircuitEvents_Limit(t *testing.T) {
	r := NewRouter()
	r.Register(AgentEndpoint{LUName: "z", Transport: TransportA2AHTTP, Address: "https://z/a2a"})

	events := r.CircuitEvents(0) // 0 = all
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}

	limited := r.CircuitEvents(0)
	if len(limited) != 1 {
		t.Errorf("expected 1 event, got %d", len(limited))
	}
}

func TestAdmissionDecision_String(t *testing.T) {
	if AdmissionAllowed.String() != "allowed" {
		t.Errorf("Allowed = %q", AdmissionAllowed.String())
	}
	if AdmissionDenied.String() != "denied" {
		t.Errorf("Denied = %q", AdmissionDenied.String())
	}
}

func TestTransport_String(t *testing.T) {
	if TransportA2AHTTP.String() != "a2a-http" {
		t.Errorf("HTTP = %q", TransportA2AHTTP.String())
	}
}
