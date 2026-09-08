package software_development

import (
	"context"
	"fmt"
	"strings"
)

const (
	Name     = "code_edit"
	Domain   = "software_development"
	Provider = "cursor"
)

// CursorRunner runs a one-shot Cursor agent against a workspace.
type CursorRunner interface {
	RunEdit(ctx context.Context, workspace, instruction string) (summary string, err error)
}

// CodeEdit edits code in the agent workspace via Cursor SDK to implement a given feature.
type CodeEdit struct {
	Runner CursorRunner
}

func (CodeEdit) Name() string { return Name }

func (CodeEdit) Domain() string { return Domain }

func (CodeEdit) Provider() string { return Provider }

func (CodeEdit) Description() string {
	return `edit code in the agent workspace to implement a feature. input: {"instruction":"<what to build>"} (or "goal"); optional "workspace"/"cwd" overrides Agent.Workspace`
}

func (c CodeEdit) Run(in map[string]string) (map[string]string, error) {
	instruction := firstNonEmpty(in["instruction"], in["goal"])
	if instruction == "" {
		return nil, fmt.Errorf("code_edit: missing instruction/goal")
	}
	workspace := firstNonEmpty(in["workspace"], in["cwd"])
	if workspace == "" {
		return nil, fmt.Errorf("code_edit: missing workspace (set Agent.Workspace or pass workspace/cwd)")
	}
	if c.Runner == nil {
		return nil, fmt.Errorf("code_edit: cursor runner not configured")
	}
	summary, err := c.Runner.RunEdit(context.Background(), workspace, instruction)
	if err != nil {
		return nil, fmt.Errorf("code_edit: %w", err)
	}
	return map[string]string{
		"status":      "ok",
		"summary":     summary,
		"workspace":   workspace,
		"provider":    Provider,
		"instruction": instruction,
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
