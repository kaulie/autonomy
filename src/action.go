package autonomy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaulie/autonomy/src/capability"
)

// Action is one concrete execution of a capability against a target.
type Action interface {
	Execute(ctx DecisionContext) error
}

type NothingAction struct{}

func (a NothingAction) Execute(ctx DecisionContext) error {
	fmt.Println("NothingAction: Execute")
	return nil
}

// SimpleAction is a legacy demo action that mutates the in-memory target asset.
type SimpleAction struct{}

func (a SimpleAction) Execute(ctx DecisionContext) error {
	assetID := ctx.Task.Target
	asset, err := _world.getWorld().assetManager.Get(assetID)
	if err != nil {
		return err
	}
	asset.State = "changed"
	_world.getWorld().assetManager.Set(assetID, asset)
	fmt.Println("SimpleAction: Execute: ", asset.State)
	return nil
}

// CapabilityAction looks up a registered capability and runs it.
type CapabilityAction struct {
	Name  string
	Input map[string]string
}

func (a CapabilityAction) Execute(ctx DecisionContext) error {
	f := activeCapabilityFactory()
	if f == nil {
		return fmt.Errorf("capability factory not ready")
	}
	name := strings.ToLower(strings.TrimSpace(a.Name))
	if name == "change" {
		name = "asset.change"
	}
	cap := f.Get(name)
	if cap == nil {
		return fmt.Errorf("unknown capability %q", a.Name)
	}
	in := copyStringMap(a.Input)
	if ctx.Agent != nil {
		if in["workspace"] == "" && in["cwd"] == "" && ctx.Agent.Workspace != "" {
			in["workspace"] = ctx.Agent.Workspace
		}
	}
	if ctx.Task != nil {
		if in["target"] == "" && ctx.Task.Target != "" {
			in["target"] = ctx.Task.Target
		}
		if in["task_id"] == "" && ctx.Task.ID != "" {
			in["task_id"] = ctx.Task.ID
		}
		if in["instruction"] == "" && in["goal"] == "" {
			if ctx.Task.Goal != "" {
				in["goal"] = ctx.Task.Goal
			} else if ctx.Task.Description != "" {
				in["instruction"] = ctx.Task.Description
			}
		}
	}
	out, err := cap.Run(in)
	if err != nil {
		return err
	}
	fmt.Printf("CapabilityAction %s: %v\n", name, out)
	return nil
}

func activeCapabilityFactory() *capability.Factory {
	if _autonomy != nil {
		return _autonomy.CapabilityFactory
	}
	return nil
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ensureAgentWorkspace creates AGENT_WORKSPACE for the agent id and returns the path with trailing separator.
func ensureAgentWorkspace(agentID string) (string, error) {
	ws := AgentWorkspacePath(agentID)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", fmt.Errorf("mkdir AGENT_WORKSPACE %s: %w", ws, err)
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return ws, nil
	}
	if len(abs) > 0 && !os.IsPathSeparator(abs[len(abs)-1]) {
		abs += string(os.PathSeparator)
	}
	return abs, nil
}
