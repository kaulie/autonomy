package clinesdk

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// Client owns one bridge and exposes the Cline surface autonomy needs.
type Client struct {
	// ProviderID / ModelID / APIKey / BaseURL are the provider defaults applied
	// to every agent this client creates.
	ProviderID string
	ModelID    string
	APIKey     string
	BaseURL    string
	// Mode is the default Cline mode: "yolo" (tools auto-approved) or "plan".
	Mode         string
	SystemPrompt string
	// Workspace is the default CWD for created agents.
	Workspace string
	// Manager owns the child process; tests override NodeBin/Script.
	Manager *BridgeManager

	mu  sync.Mutex
	tr  *transport
	inf *BridgeInfo
}

// ClientOption configures a Client.
type ClientOption func(*Client)

func WithProvider(id string) ClientOption    { return func(c *Client) { c.ProviderID = id } }
func WithModel(id string) ClientOption       { return func(c *Client) { c.ModelID = id } }
func WithAPIKey(key string) ClientOption     { return func(c *Client) { c.APIKey = key } }
func WithBaseURL(url string) ClientOption    { return func(c *Client) { c.BaseURL = url } }
func WithMode(mode string) ClientOption      { return func(c *Client) { c.Mode = mode } }
func WithWorkspace(path string) ClientOption { return func(c *Client) { c.Workspace = path } }
func WithSystemPrompt(p string) ClientOption { return func(c *Client) { c.SystemPrompt = p } }

// WithManager replaces the bridge process manager (tests, or an externally
// managed bridge).
func WithManager(m *BridgeManager) ClientOption { return func(c *Client) { c.Manager = m } }

// NewClient builds a client from options plus environment defaults.
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		ProviderID: strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_PROVIDER")),
		ModelID:    strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_MODEL")),
		APIKey:     strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_API_KEY")),
		BaseURL:    strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_BASE_URL")),
		Mode:       DefaultMode,
		Manager:    &BridgeManager{},
	}
	if c.Workspace == "" {
		if wd, err := os.Getwd(); err == nil {
			c.Workspace = wd
		}
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// DefaultMode runs a Cline session with tools enabled and auto-approved, which
// is what an autonomy capability prompt expects.
const DefaultMode = "yolo"

// ensure starts the bridge on first use and returns the transport.
func (c *Client) ensure(ctx context.Context) (*transport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tr != nil {
		return c.tr, nil
	}
	manager := c.Manager
	if manager == nil {
		manager = &BridgeManager{}
	}
	info, err := manager.Start(ctx)
	if err != nil {
		return nil, err
	}
	if manager.transport == nil {
		return nil, bridgeErr("bridge started without a transport")
	}
	c.tr = manager.transport
	c.inf = info
	return c.tr, nil
}

// Ping returns the bridge handshake plus the provider ids the SDK knows.
func (c *Client) Ping(ctx context.Context) (*BridgeInfo, []string, error) {
	tr, err := c.ensure(ctx)
	if err != nil {
		return nil, nil, err
	}
	raw, err := tr.call(ctx, "ping", map[string]any{})
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Protocol  string   `json:"protocol"`
		Node      string   `json:"node"`
		SDK       string   `json:"sdk"`
		PID       int      `json:"pid"`
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, nil, bridgeErr("decode ping: %v", err)
	}
	info := &BridgeInfo{Protocol: res.Protocol, Node: res.Node, SDK: res.SDK, PID: res.PID}
	return info, res.Providers, nil
}

// Model is one provider model entry.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// Models lists the models a provider exposes.
func (c *Client) Models(ctx context.Context, providerID string) ([]Model, error) {
	tr, err := c.ensure(ctx)
	if err != nil {
		return nil, err
	}
	if providerID == "" {
		providerID = c.ProviderID
	}
	raw, err := tr.call(ctx, "models", map[string]any{"providerId": providerID})
	if err != nil {
		return nil, err
	}
	var res struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, bridgeErr("decode models: %v", err)
	}
	return res.Models, nil
}

// Agents returns the agent factory bound to this client.
func (c *Client) Agents() *AgentFactory { return &AgentFactory{client: c} }

// Close shuts the bridge down.
func (c *Client) Close() error {
	c.mu.Lock()
	manager := c.Manager
	c.tr = nil
	c.mu.Unlock()
	if manager == nil {
		return nil
	}
	manager.Stop()
	return nil
}
