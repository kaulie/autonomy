package cursorsdk

import (
	"context"

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
	stream, err := a.client.agentRPC.Send(ctx, connect.NewRequest(&sdkv1.SendRequest{
		AgentId: a.ID,
		Message: &sdkv1.UserMessage{Text: text},
	}))
	if err != nil {
		return nil, wrapConnectErr(err)
	}
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
