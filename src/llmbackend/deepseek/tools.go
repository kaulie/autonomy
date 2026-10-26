package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The tools a DeepSeek session gives the model: the same kit the other harnesses hand a
// coding agent (Claude's Read/Edit/Write/Glob/Grep/Bash), executed here, inside the agent's
// workspace. A planner (plan mode) gets the read-only half, as Claude's plan mode does.

const (
	toolReadFile    = "read_file"
	toolWriteFile   = "write_file"
	toolEditFile    = "edit_file"
	toolListFiles   = "list_files"
	toolSearchFiles = "search_files"
	toolRunShell    = "run_shell"

	// maxToolOutput bounds what one tool hands back to the model.
	maxToolOutput = 64 * 1024
	// maxListed bounds a listing or a search.
	maxListed = 500
	// defaultShellTimeout bounds one shell command unless the model asks for less/more.
	defaultShellTimeout = 120 * time.Second
	maxShellTimeout     = 600 * time.Second
)

func param(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func object(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func spec(name, desc string, params map[string]any) toolSpec {
	return toolSpec{Type: "function", Function: functionSpec{Name: name, Description: desc, Parameters: params}}
}

// toolSpecs is the tool list a turn in this mode sends.
func toolSpecs(mode llmbackend.Mode) []toolSpec {
	specs := []toolSpec{
		spec(toolReadFile, "Read a text file in the workspace. Optional 1-based offset/limit select lines.",
			object([]string{"path"}, map[string]any{
				"path":   param("string", "file path, relative to the workspace"),
				"offset": param("integer", "first line to return (1-based)"),
				"limit":  param("integer", "how many lines to return"),
			})),
		spec(toolListFiles, "List files under a workspace directory (recursive), optionally filtered by a glob on the file name.",
			object(nil, map[string]any{
				"path":    param("string", "directory, relative to the workspace (default: the workspace)"),
				"pattern": param("string", "glob matched against each file's base name, e.g. *.go"),
			})),
		spec(toolSearchFiles, "Search file contents under a workspace directory with a regular expression; returns path:line: text.",
			object([]string{"pattern"}, map[string]any{
				"pattern": param("string", "regular expression (Go RE2 syntax)"),
				"path":    param("string", "directory or file, relative to the workspace (default: the workspace)"),
				"glob":    param("string", "only search files whose base name matches this glob"),
			})),
	}
	if mode == llmbackend.ModePlan {
		return specs
	}
	return append(specs,
		spec(toolWriteFile, "Create or overwrite a file in the workspace with the given content (parent directories are created).",
			object([]string{"path", "content"}, map[string]any{
				"path":    param("string", "file path, relative to the workspace"),
				"content": param("string", "the complete new file content"),
			})),
		spec(toolEditFile, "Replace an exact string in a workspace file. old_string must occur exactly once unless replace_all is true.",
			object([]string{"path", "old_string", "new_string"}, map[string]any{
				"path":        param("string", "file path, relative to the workspace"),
				"old_string":  param("string", "the exact text to replace"),
				"new_string":  param("string", "the replacement text"),
				"replace_all": param("boolean", "replace every occurrence"),
			})),
		spec(toolRunShell, "Run a shell command (bash -c) in the workspace and return its combined output and exit code.",
			object([]string{"command"}, map[string]any{
				"command":         param("string", "the command line"),
				"timeout_seconds": param("integer", "time limit (default 120, max 600)"),
			})),
	)
}

// toolbox runs tool calls in one workspace.
type toolbox struct {
	root string
	mode llmbackend.Mode
}

// run executes one call and reports its output; isError says the model should treat the
// output as a failure (a bad argument, a missing file, a non-zero exit).
func (t toolbox) run(ctx context.Context, name, arguments string) (string, bool) {
	var args map[string]any
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "invalid tool arguments (want a JSON object): " + err.Error(), true
	}
	allowed := false
	for _, s := range toolSpecs(t.mode) {
		if s.Function.Name == name {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Sprintf("tool %q is not available in %s mode", name, t.mode), true
	}
	var out string
	var err error
	switch name {
	case toolReadFile:
		out, err = t.readFile(args)
	case toolWriteFile:
		out, err = t.writeFile(args)
	case toolEditFile:
		out, err = t.editFile(args)
	case toolListFiles:
		out, err = t.listFiles(args)
	case toolSearchFiles:
		out, err = t.searchFiles(args)
	case toolRunShell:
		return t.runShell(ctx, args)
	}
	if err != nil {
		return err.Error(), true
	}
	return truncate(out), false
}

func truncate(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + fmt.Sprintf("\n… [truncated: %d more bytes]", len(s)-maxToolOutput)
}

func str(args map[string]any, key string) string { s, _ := args[key].(string); return s }

func num(args map[string]any, key string) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return 0
}

// resolve maps a model-supplied path onto the workspace and refuses one that leaves it.
func (t toolbox) resolve(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(t.root, full)
	}
	full = filepath.Clean(full)
	rel, err := filepath.Rel(t.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace %s", path, t.root)
	}
	return full, nil
}

func (t toolbox) display(full string) string {
	if rel, err := filepath.Rel(t.root, full); err == nil {
		return filepath.ToSlash(rel)
	}
	return full
}
