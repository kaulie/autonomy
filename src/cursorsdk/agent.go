package cursorsdk

import (
	"context"
	"time"

	"connectrpc.com/connect"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
)

// Agent is one agent handle (local or resumed).
type Agent struct {
	client *Client
	ID     string
	Model  string
}

// Send sends a user message and returns a Run streaming events.
func (a *Agent) Send(ctx context.Context, text string) (*Run, error) {
	if err := a.client.ensure(); err != nil {
		return nil, err
	}
	Trace("Send", "rpc begin agent_id=%s prompt_bytes=%d", a.ID, len(text))
	started := time.Now()
	stream, err := a.client.agentRPC.Send(ctx, connect.NewRequest(&sdkv1.SendRequest{
		AgentId: a.ID,
		Message: &sdkv1.UserMessage{Text: text},
	}))
	if err != nil {
		Trace("Send", "rpc error after %s: %v", time.Since(started).Round(time.Millisecond), err)
		return nil, wrapConnectErr(err)
	}
	Trace("Send", "stream opened elapsed=%s (next: Wait drains events)", time.Since(started).Round(time.Millisecond))
	return newRun(a.client, a.ID, stream), nil
}

// Close releases local resources. Durable state is kept.
func (a *Agent) Close(ctx context.Context) error {
	if err := a.client.ensure(); err != nil {
		return err
	}
	_, err := a.client.agentRPC.CloseAgent(ctx, connect.NewRequest(&sdkv1.CloseAgentRequest{AgentId: a.ID}))
	return wrapConnectErr(err)
}

// Delete permanently removes the agent and its durable data via DeleteAgent.
func (a *Agent) Delete(ctx context.Context) error {
	if err := a.client.ensure(); err != nil {
		return err
	}
	Trace("DeleteAgent", "rpc begin agent_id=%s", a.ID)
	started := time.Now()
	_, err := a.client.agentRPC.DeleteAgent(ctx, connect.NewRequest(&sdkv1.DeleteAgentRequest{
		AgentId: a.ID,
	}))
	if err != nil {
		Trace("DeleteAgent", "rpc error after %s: %v", time.Since(started).Round(time.Millisecond), err)
		return wrapConnectErr(err)
	}
	Trace("DeleteAgent", "rpc ok elapsed=%s", time.Since(started).Round(time.Millisecond))
	return nil
}

// Resume re-attaches to an existing agent id.
func (f *AgentFactory) Resume(ctx context.Context, agentID, model string) (*Agent, error) {
	if err := f.client.ensure(); err != nil {
		return nil, err
	}
	req := &sdkv1.ResumeAgentRequest{
		AgentId: agentID,
		Options: &sdkv1.AgentOptions{
			Model:  &sdkv1.ModelSelection{Id: model},
			ApiKey: f.client.APIKey,
			Local:  &sdkv1.LocalAgentOptions{Cwd: []string{f.client.Workspace}},
		},
	}
	resp, err := f.client.agentRPC.ResumeAgent(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, wrapConnectErr(err)
	}
	id := resp.Msg.GetAgentId()
	if id == "" {
		id = agentID
	}
	return &Agent{client: f.client, ID: id, Model: model}, nil
}
