package cursorsdk

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"connectrpc.com/connect"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1/sdkv1connect"
)

// BridgeVersion is reported by SdkBridgeControlService.GetVersion.
type BridgeVersion struct {
	BridgeVersion   string
	ProtocolVersion string
	Capabilities    []string
}

// Model is one catalog model.
type Model struct {
	ID          string
	DisplayName string
	Description string
}

// Client owns one bridge (spawned lazily) and exposes the SDK surface.
type Client struct {
	APIKey    string
	Workspace string
	BridgeBin string

	// Optional: attach to an already-running bridge instead of spawning.
	Endpoint  string
	AuthToken string

	mu       sync.Mutex
	manager  *BridgeManager
	http     *http.Client
	baseURL  string
	agentRPC sdkv1connect.SdkAgentServiceClient
	ctrlRPC  sdkv1connect.SdkBridgeControlServiceClient
	curRPC   sdkv1connect.SdkCursorServiceClient
	ready    bool
}

// ClientOption configures a Client.
type ClientOption func(*Client)

func WithAPIKey(key string) ClientOption {
	return func(c *Client) { c.APIKey = key }
}

func WithWorkspace(path string) ClientOption {
	return func(c *Client) { c.Workspace = mustAbs(path) }
}

func WithBridgeBin(path string) ClientOption {
	return func(c *Client) { c.BridgeBin = path }
}

func WithEndpoint(url, token string) ClientOption {
	return func(c *Client) {
		c.Endpoint = url
		c.AuthToken = token
	}
}

func mustAbs(p string) string {
	if p == "" || p == "." {
		a, _ := filepath.Abs(".")
		return a
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

// NewClient constructs a Client. The bridge is started on first RPC.
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		APIKey:    os.Getenv("CURSOR_API_KEY"),
		Workspace: mustAbs("."),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) ensure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready {
		return nil
	}

	var ep *BridgeEndpoint
	if c.Endpoint != "" || c.AuthToken != "" {
		if c.Endpoint == "" || c.AuthToken == "" {
			return sdkErr("endpoint and auth_token must be provided together")
		}
		ep = &BridgeEndpoint{URL: c.Endpoint, AuthToken: c.AuthToken}
	} else {
		c.manager = &BridgeManager{
			Binary:    c.BridgeBin,
			Workspace: c.Workspace,
			APIKey:    c.APIKey,
		}
		var err error
		ep, err = c.manager.Start()
		if err != nil {
			return err
		}
	}

	c.http = newBridgeHTTPClient(ep.AuthToken)
	c.baseURL = ep.URL
	c.agentRPC = sdkv1connect.NewSdkAgentServiceClient(c.http, c.baseURL, connectOpts()...)
	c.ctrlRPC = sdkv1connect.NewSdkBridgeControlServiceClient(c.http, c.baseURL, connectOpts()...)
	c.curRPC = sdkv1connect.NewSdkCursorServiceClient(c.http, c.baseURL, connectOpts()...)
	c.ready = true
	return nil
}

// Close shuts down a managed bridge. No-op when attached to an external endpoint.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manager != nil {
		if c.ctrlRPC != nil {
			_, _ = c.ctrlRPC.Shutdown(context.Background(), connect.NewRequest(&sdkv1.ShutdownRequest{}))
		}
		c.manager.Stop()
		c.manager = nil
	}
	c.ready = false
	return nil
}

// Ping checks bridge liveness.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.ensure(); err != nil {
		return err
	}
	_, err := c.ctrlRPC.Ping(ctx, connect.NewRequest(&sdkv1.PingRequest{}))
	return wrapConnectErr(err)
}

// Version returns bridge/protocol/capabilities.
func (c *Client) Version(ctx context.Context) (*BridgeVersion, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	resp, err := c.ctrlRPC.GetVersion(ctx, connect.NewRequest(&sdkv1.GetVersionRequest{}))
	if err != nil {
		return nil, wrapConnectErr(err)
	}
	msg := resp.Msg
	return &BridgeVersion{
		BridgeVersion:   msg.GetBridgeVersion(),
		ProtocolVersion: msg.GetProtocolVersion(),
		Capabilities:    append([]string(nil), msg.GetCapabilities()...),
	}, nil
}

// Agents returns the agent factory bound to this client.
func (c *Client) Agents() *AgentFactory { return &AgentFactory{client: c} }

// Cursor returns the catalog API (Me / Models / Repositories).
func (c *Client) Cursor() *CursorCatalog { return &CursorCatalog{client: c} }

// AgentFactory creates and resumes agents.
type AgentFactory struct{ client *Client }

// CreateOptions configures AgentFactory.Create.
type CreateOptions struct {
	Model string
	CWD   string
	Mode  sdkv1.AgentModeOption
}

// Create creates a local agent.
func (f *AgentFactory) Create(ctx context.Context, opts CreateOptions) (*Agent, error) {
	if err := f.client.ensure(); err != nil {
		return nil, err
	}
	if opts.Model == "" {
		return nil, &ValidationError{RpcError: RpcError{Code: "invalid_argument", Message: "model is required"}}
	}
	cwd := opts.CWD
	if cwd == "" {
		cwd = f.client.Workspace
	}
	req := &sdkv1.CreateAgentRequest{
		Options: &sdkv1.AgentOptions{
			Model:  &sdkv1.ModelSelection{Id: opts.Model},
			ApiKey: f.client.APIKey,
			Local:  &sdkv1.LocalAgentOptions{Cwd: []string{cwd}},
			Mode:   opts.Mode,
		},
	}
	resp, err := f.client.agentRPC.CreateAgent(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, wrapConnectErr(err)
	}
	return &Agent{client: f.client, ID: resp.Msg.GetAgentId(), Model: opts.Model}, nil
}

// CursorCatalog wraps SdkCursorService.
type CursorCatalog struct{ client *Client }

// Models lists available models (requires API key on the call).
func (c *CursorCatalog) Models(ctx context.Context) ([]Model, error) {
	if err := c.client.ensure(); err != nil {
		return nil, err
	}
	resp, err := c.client.curRPC.ListModels(ctx, connect.NewRequest(&sdkv1.ListModelsRequest{
		Options: &sdkv1.CursorRequestOptions{ApiKey: c.client.APIKey},
	}))
	if err != nil {
		return nil, wrapConnectErr(err)
	}
	out := make([]Model, 0, len(resp.Msg.GetItems()))
	for _, m := range resp.Msg.GetItems() {
		out = append(out, Model{
			ID:          m.GetId(),
			DisplayName: m.GetDisplayName(),
			Description: m.GetDescription(),
		})
	}
	return out, nil
}

func wrapConnectErr(err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := err.(*connect.Error); ok {
		return mapRpcError(ce.Code().String(), ce.Message(), "", "")
	}
	return &TransportError{Msg: err.Error()}
}

// Prompt is the one-shot helper: create → send → wait → close.
func Prompt(ctx context.Context, text string, opts ...ClientOption) (string, error) {
	client := NewClient(opts...)
	defer client.Close()

	model := os.Getenv("AUTONOMY_LLM_MODEL")
	if model == "" {
		models, err := client.Cursor().Models(ctx)
		if err != nil {
			return "", err
		}
		if len(models) == 0 {
			return "", sdkErr("no models available to this account")
		}
		model = models[0].ID
	}

	agent, err := client.Agents().Create(ctx, CreateOptions{Model: model})
	if err != nil {
		return "", err
	}
	defer agent.Close(ctx)

	run, err := agent.Send(ctx, text)
	if err != nil {
		return "", err
	}
	result, err := run.Wait(ctx)
	if err != nil {
		return "", err
	}
	if !result.OK() {
		return "", fmt.Errorf("run %s: %s", result.Status, result.ErrorMessage)
	}
	return result.Text, nil
}
