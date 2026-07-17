// Package vtam implements MACS §8: Protocol admission control and
// multi-transport routing for multi-agent networks.
//
// Agents communicate over heterogeneous transports (HTTP, gRPC,
// WebSocket, stdio). Each transport has different semantics.
// Agents should not care about transport details — they should care
// about the agent they are talking to.
//
// z/OS VTAM (1974) solved this with Logical Unit (LU) names:
// applications talked to LU names; VTAM handled SNA, TCP/IP, X.25
// transparently. This package applies the same principle to A2A, MCP,
// and Feishu transports.
package vtam

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Transport is a protocol that agents can use to communicate.
type Transport string

const (
	TransportA2AHTTP      Transport = "a2a-http"
	TransportA2AgRPC      Transport = "a2a-grpc"
	TransportA2AWebSocket Transport = "a2a-websocket"
	TransportMCPStdio     Transport = "mcp-stdio"
	TransportMCPHTTP      Transport = "mcp-http"
	TransportFeishu       Transport = "feishu"
)

// String returns the transport name.
func (t Transport) String() string { return string(t) }

// Priority returns the preference order (lower = preferred).
func (t Transport) Priority() int {
	switch t {
	case TransportA2AgRPC:
		return 0
	case TransportA2AHTTP:
		return 1
	case TransportA2AWebSocket:
		return 2
	case TransportMCPHTTP:
		return 3
	case TransportMCPStdio:
		return 4
	case TransportFeishu:
		return 5
	default:
		return 99
	}
}

// LUName is a Logical Unit name — a stable agent identifier independent
// of transport. Maps to VTAM's LU name concept.
type LUName string

// AgentEndpoint describes how to reach an agent via a specific transport.
type AgentEndpoint struct {
	LUName   LUName    // stable identifier
	Transport Transport // which protocol
	Address  string    // "https://agent.example.com/a2a" or "grpc://agent:50051"
	Healthy  bool      // circuit-level health
	LastSeen time.Time
}

// AdmissionRule controls which agents can communicate.
type AdmissionRule struct {
	SourceAgent    LUName        // who wants to connect
	TargetAgent    LUName        // who they want to reach
	AllowedMethods []string      // "tasks/send", "tasks/get", etc.
	MaxRate        int           // requests per minute (0 = unlimited)
	RequireAuth    bool          // mutual TLS / API key required
	AllowedAfter   string        // "09:00" — time window start
	AllowedBefore  string        // "17:00" — time window end
}

// AdmissionDecision is the result of an admission check.
type AdmissionDecision int

const (
	AdmissionAllowed AdmissionDecision = iota
	AdmissionDenied
	AdmissionRateLimited
)

// String returns the decision name.
func (d AdmissionDecision) String() string {
	switch d {
	case AdmissionAllowed:
		return "allowed"
	case AdmissionDenied:
		return "denied"
	case AdmissionRateLimited:
		return "rate_limited"
	default:
		return fmt.Sprintf("unknown(%d)", d)
	}
}

// Router manages agent LU name resolution and transport selection.
type Router struct {
	mu        sync.RWMutex
	endpoints map[LUName][]AgentEndpoint
	rules     []AdmissionRule
	events    []CircuitEvent
}

// CircuitEvent records a transport-level event for observability.
type CircuitEvent struct {
	Timestamp       time.Time
	SourceLU        LUName
	TargetLU        LUName
	Transport       Transport
	Event           string // "connected", "disconnected", "failed", "selected"
	SelectionReason string // why this transport was chosen
	Latency         time.Duration
}

// NewRouter creates a VTAM router.
func NewRouter() *Router {
	return &Router{
		endpoints: make(map[LUName][]AgentEndpoint),
	}
}

// Register adds an agent endpoint with a transport.
func (r *Router) Register(ep AgentEndpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep.LastSeen = time.Now()
	ep.Healthy = true
	r.endpoints[ep.LUName] = append(r.endpoints[ep.LUName], ep)
	r.events = append(r.events, CircuitEvent{
		Timestamp: time.Now(),
		SourceLU:  "", // registration, not connection
		TargetLU:  ep.LUName,
		Transport: ep.Transport,
		Event:     "registered",
	})
}

// Resolve finds all available transports for an agent LU name.
func (r *Router) Resolve(lu LUName) []AgentEndpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.endpoints[lu]
}

// Route selects the best transport to reach a target agent.
// Returns the endpoint and a selection reason.
func (r *Router) Route(source, target LUName, preferredMethods []string) (*AgentEndpoint, string, error) {
	r.mu.RLock()
	endpoints := r.endpoints[target]
	r.mu.RUnlock()

	if len(endpoints) == 0 {
		return nil, "", fmt.Errorf("vtam: no endpoint registered for LU %q", target)
	}

	// Filter healthy endpoints
	var healthy []AgentEndpoint
	for _, ep := range endpoints {
		if ep.Healthy {
			healthy = append(healthy, ep)
		}
	}
	if len(healthy) == 0 {
		return nil, "", fmt.Errorf("vtam: all endpoints unhealthy for LU %q", target)
	}

	// Sort by transport priority (gRPC > HTTP > WebSocket > stdio)
	sort.Slice(healthy, func(i, j int) bool {
		return healthy[i].Transport.Priority() < healthy[j].Transport.Priority()
	})

	selected := &healthy[0]
	reason := fmt.Sprintf("selected %s (priority=%d)", selected.Transport, selected.Transport.Priority())

	// Record circuit event
	r.mu.Lock()
	r.events = append(r.events, CircuitEvent{
		Timestamp:       time.Now(),
		SourceLU:        source,
		TargetLU:        target,
		Transport:       selected.Transport,
		Event:           "selected",
		SelectionReason: reason,
	})
	r.mu.Unlock()

	return selected, reason, nil
}

// AddRule registers an admission rule.
func (r *Router) AddRule(rule AdmissionRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules = append(r.rules, rule)
}

// CheckAdmission evaluates whether source can reach target.
func (r *Router) CheckAdmission(source, target LUName, method string) AdmissionDecision {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := time.Now()

	for _, rule := range r.rules {
		if rule.SourceAgent != source || rule.TargetAgent != target {
			continue
		}

		// Time window check
		if rule.AllowedAfter != "" && rule.AllowedBefore != "" {
			after, _ := time.Parse("15:04", rule.AllowedAfter)
			before, _ := time.Parse("15:04", rule.AllowedBefore)
			current := time.Date(0, 1, 1, now.Hour(), now.Minute(), 0, 0, time.UTC)
			if current.Before(after) || current.After(before) {
				return AdmissionDenied
			}
		}

		// Method check
		if len(rule.AllowedMethods) > 0 {
			allowed := false
			for _, m := range rule.AllowedMethods {
				if m == method {
					allowed = true
					break
				}
			}
			if !allowed {
				return AdmissionDenied
			}
		}

		// Rate limiting: simplified — in production, use a sliding window
		if rule.MaxRate > 0 {
			count := r.countRecentConnections(source, target, time.Minute)
			if count >= rule.MaxRate {
				return AdmissionRateLimited
			}
		}
	}

	return AdmissionAllowed
}

// MarkUnhealthy marks an endpoint as unhealthy.
func (r *Router) MarkUnhealthy(lu LUName, transport Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, ep := range r.endpoints[lu] {
		if ep.Transport == transport {
			r.endpoints[lu][i].Healthy = false
			r.events = append(r.events, CircuitEvent{
				Timestamp: time.Now(),
				TargetLU:  lu,
				Transport: transport,
				Event:     "failed",
			})
			return
		}
	}
}

// CircuitEvents returns recent circuit-level events.
func (r *Router) CircuitEvents(limit int) []CircuitEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if limit <= 0 || limit > len(r.events) {
		limit = len(r.events)
	}
	start := len(r.events) - limit
	if start < 0 {
		start = 0
	}
	result := make([]CircuitEvent, limit)
	copy(result, r.events[start:])
	return result
}

// countRecentConnections counts connections in the last window.
func (r *Router) countRecentConnections(source, target LUName, window time.Duration) int {
	cutoff := time.Now().Add(-window)
	count := 0
	for _, ev := range r.events {
		if ev.Timestamp.Before(cutoff) {
			continue
		}
		if ev.SourceLU == source && ev.TargetLU == target && ev.Event == "selected" {
			count++
		}
	}
	return count
}
