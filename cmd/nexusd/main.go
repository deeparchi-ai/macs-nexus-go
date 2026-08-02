// Command nexusd is the Feishu-native MACS deployment skeleton:
// a long-running service that receives Feishu webhook events, normalizes
// them through the Nexus Feishu adapter, applies the L0–L3 governance
// policy gate, and routes actionable mentions to target agents via the
// A2A bridge.
//
// This is the "Feishu version of QM" control plane: core Nexus packages
// (vtam/feishu/bridge) stay untouched; all deployment-specific wiring
// (registry, chat layers, permissions, agent endpoints) lives in config.
//
// Usage:
//
//	nexusd -config config.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/deeparchi-ai/macs-vtam-go/pkg/bridge"
	"github.com/deeparchi-ai/macs-vtam-go/pkg/feishu"
	"github.com/deeparchi-ai/macs-vtam-go/pkg/vtam"
)

// Config is the deployment-layer configuration. It encodes everything
// that varies per customer/org: the bot registry, chat layer mapping,
// the governance permission matrix, and agent A2A endpoints.
type Config struct {
	// ListenAddr is the HTTP bind address (default ":8080").
	ListenAddr string `yaml:"listen_addr"`
	// Bots maps Feishu bot open_ids to agent LU names.
	Bots map[string]string `yaml:"bots"`
	// Layers maps chat_id → governance layer (L0..L3).
	Layers map[string]string `yaml:"layers"`
	// DefaultLayer applies to chats not in Layers.
	DefaultLayer string `yaml:"default_layer"`
	// Permissions is the governance matrix: lu → layer → level.
	Permissions map[string]map[string]string `yaml:"permissions"`
	// Endpoints registers agent A2A endpoints in the router.
	Endpoints []EndpointConfig `yaml:"endpoints"`
}

// EndpointConfig describes one agent transport endpoint.
type EndpointConfig struct {
	LU        string `yaml:"lu"`
	Transport string `yaml:"transport"` // a2a-http | a2a-grpc
	Address   string `yaml:"address"`
}

// loadConfig reads and validates the deployment config.
func loadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.DefaultLayer == "" {
		cfg.DefaultLayer = "L3"
	}
	return &cfg, nil
}

// buildWiring turns config into the Nexus wiring: registry, policy, router.
// It returns the chat-layer resolver so the server can tag events.
func buildWiring(cfg *Config) (*feishu.Registry, *feishu.Policy, *vtam.Router, func(string) string) {
	reg := feishu.NewRegistry()
	for openID, lu := range cfg.Bots {
		reg.RegisterBot(openID, lu)
	}

	layerOf := func(chatID string) string {
		if l, ok := cfg.Layers[chatID]; ok {
			return l
		}
		return cfg.DefaultLayer
	}

	perm := func(lu, layer string) feishu.Permission {
		layerPerms, ok := cfg.Permissions[lu]
		if !ok {
			return feishu.PermissionForbidden
		}
		level, ok := layerPerms[layer]
		if !ok {
			return feishu.PermissionForbidden
		}
		switch level {
		case "allowed":
			return feishu.PermissionAllowed
		case "mention_only":
			return feishu.PermissionMentionOnly
		case "on_demand":
			return feishu.PermissionOnDemand
		case "owner":
			return feishu.PermissionOwner
		default:
			return feishu.PermissionForbidden
		}
	}

	policy := feishu.NewPolicy(reg, layerOf, perm)

	router := vtam.NewRouter()
	for _, ep := range cfg.Endpoints {
		router.Register(vtam.AgentEndpoint{
			LUName:    vtam.LUName(ep.LU),
			Transport: vtam.Transport(ep.Transport),
			Address:   ep.Address,
		})
	}

	return reg, policy, router, layerOf
}

// BridgeSender is the outbound A2A interface used by the server.
// The production implementation is bridge.Bridge; tests inject a mock.
type BridgeSender interface {
	Send(ctx context.Context, evt feishu.Event, target vtam.LUName) (string, error)
}

// server wires the Nexus pipeline behind an HTTP handler.
type server struct {
	policy  *feishu.Policy
	bridge  BridgeSender
	layerOf func(chatID string) string
	log     *log.Logger
}

// handleWebhook is the Feishu event-subscription callback. It runs the
// full ingress pipeline: parse → resolve → policy gate → A2A route.
func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	evt, err := feishu.ParseWebhook(body)
	if err != nil {
		// Non-message events (e.g. chat updates) are ack'd silently —
		// Feishu expects a 200 for events we don't act on.
		s.log.Printf("ignored event: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	s.log.Printf("event received: chat=%s sender=%s text=%q",
		evt.ChatID, evt.Sender, truncate(evt.Text, 60))

	// Resolve sender and mentions through the registry.
	evt.ResolveSenderLU(s.policy.Registry())
	evt.ApplyChatLayer(s.layerOf)

	// Governance gate: mentioned agents must be permitted in this layer.
	ctx := context.Background()
	for _, targetLU := range s.policy.ResolveMentions(evt.Text) {
		allowed, reason := s.policy.Allowed(targetLU, evt.ChatID, true)
		if !allowed {
			s.log.Printf("policy denied: %s -> %s (%s)", targetLU, evt.ChatID, reason)
			continue
		}
		s.log.Printf("routing mention: %s -> %s", targetLU, evt.ChatID)
		if id, err := s.bridge.Send(ctx, *evt, vtam.LUName(targetLU)); err != nil {
			s.log.Printf("bridge error: %s: %v", targetLU, err)
		} else {
			s.log.Printf("routed ok: %s -> task %s", targetLU, id)
		}
	}

	// Feishu expects a prompt 200 ack.
	w.WriteHeader(http.StatusOK)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func main() {
	configPath := flag.String("config", "config.yaml", "deployment config path")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	_, policy, router, layerOf := buildWiring(cfg)

	b := bridge.New(router)
	srv := &server{policy: policy, bridge: b, layerOf: layerOf, log: log.Default()}

	mux := http.NewServeMux()
	mux.HandleFunc("/feishu/webhook", srv.handleWebhook)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	log.Printf("nexusd listening on %s (bots=%d, endpoints=%d)",
		cfg.ListenAddr, len(cfg.Bots), len(cfg.Endpoints))
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}
