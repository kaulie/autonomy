package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// The `context` subcommand is the Context Service's operator write path
// (docs/http-api.md):
//
//	POST /api/context/resources               register (autonomy context register)
//	POST /api/context/resources/{id}/sync     index it  (autonomy context sync <id>, or register --sync)
//
//	autonomy context register --project project-749a0238 --name runtime.md \
//	    --source-type git --location /srv/autonomy --path docs/runtime.md --sync
//	autonomy context sync ctx-1a2b3c4d5e6f7a8b
//
// Each step prints the runtime's JSON answer; a refusal ({code, message}) is a
// non-zero exit. Indexing needs the runtime on the postgres store engine, and
// only document (Markdown) resources produce sections.

const contextUsage = `usage:
  autonomy context register --project <id> --name <name> [--type document|repository|service]
      [--source-type local_file|git] [--location <path>] [--path <file>] [--uri <uri>] [--id <id>] [--sync]
  autonomy context sync <resource-id>
`

// contextResourcesPath is POST /api/context/resources.
const contextResourcesPath = "/api/context/resources"

func contextSyncPath(resourceID string) string {
	return contextResourcesPath + "/" + url.PathEscape(resourceID) + "/sync"
}

// contextResourceRequest is the register body, as the runtime reads it.
type contextResourceRequest struct {
	ID        string            `json:"id,omitempty"`
	ProjectID string            `json:"project_id"`
	Type      string            `json:"type,omitempty"`
	Name      string            `json:"name,omitempty"`
	URI       string            `json:"uri,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Source    contextSource     `json:"source"`
}

type contextSource struct {
	Type     string `json:"type,omitempty"`
	Location string `json:"location,omitempty"`
	Path     string `json:"path,omitempty"`
}

// contextCLI runs `autonomy context …` (args are what follows "context").
func contextCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, contextUsage)
		return 2
	}
	server := envOr("AUTONOMY_API_URL", defaultServer)
	fs := flag.NewFlagSet("autonomy context "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, contextUsage)
		fs.PrintDefaults()
	}
	fs.StringVar(&server, "server", server, "runtime base URL (env AUTONOMY_API_URL)")

	switch args[0] {
	case "register":
		var body contextResourceRequest
		var sync bool
		fs.StringVar(&body.ProjectID, "project", "", "project the resource belongs to (required)")
		fs.StringVar(&body.Name, "name", "", "resource name")
		fs.StringVar(&body.Type, "type", "document", "document | repository | service")
		fs.StringVar(&body.Source.Type, "source-type", "local_file", "local_file | git")
		fs.StringVar(&body.Source.Location, "location", "", "file, or directory with --path, on the runtime's host")
		fs.StringVar(&body.Source.Path, "path", "", "file inside --location")
		fs.StringVar(&body.URI, "uri", "", "resource URI")
		fs.StringVar(&body.ID, "id", "", "resource id; empty lets the runtime mint one, an existing id re-registers")
		fs.BoolVar(&sync, "sync", false, "index the resource right after registering it")
		if code, done := parseContextFlags(fs, args[1:], 0, stderr); done {
			return code
		}
		if strings.TrimSpace(body.ProjectID) == "" {
			fmt.Fprintln(stderr, "context register: --project is required")
			return 2
		}
		c, err := newClient(server)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		var resource struct {
			ID string `json:"id"`
		}
		var raw json.RawMessage
		if err := c.post(ctx, contextResourcesPath, body, &raw); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, raw)
		if !sync {
			return 0
		}
		if err := json.Unmarshal(raw, &resource); err != nil || resource.ID == "" {
			fmt.Fprintln(stderr, "context register: the runtime answered no resource id to sync")
			return 1
		}
		return contextSync(ctx, c, resource.ID, stdout, stderr)
	case "sync":
		if code, done := parseContextFlags(fs, args[1:], 1, stderr); done {
			return code
		}
		c, err := newClient(server)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return contextSync(ctx, c, fs.Arg(0), stdout, stderr)
	default:
		fmt.Fprintf(stderr, "context: unknown command %q\n%s", args[0], contextUsage)
		return 2
	}
}

// parseContextFlags parses one subcommand's flags and checks it got exactly
// positional positional arguments; done reports that the caller should exit
// with code.
func parseContextFlags(fs *flag.FlagSet, args []string, positional int, stderr io.Writer) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	if fs.NArg() != positional {
		fmt.Fprint(stderr, contextUsage)
		return 2, true
	}
	return 0, false
}

// contextSync is POST /api/context/resources/{id}/sync, printed as answered.
func contextSync(ctx context.Context, c *client, resourceID string, stdout, stderr io.Writer) int {
	var raw json.RawMessage
	if err := c.post(ctx, contextSyncPath(resourceID), nil, &raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, raw)
	return 0
}
