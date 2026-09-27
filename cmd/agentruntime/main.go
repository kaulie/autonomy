// Command agentruntime asks a running Autonomy runtime which model backs an agent.
//
// It is a thin client for GET /api/agents/{id}/runtime (src/agent_runtime_resolve.go):
//
//	go run ./cmd/agentruntime -agent 10038
//	AUTONOMY_API_URL=http://127.0.0.1:4300 go run ./cmd/agentruntime -agent 10038
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultServer = "http://127.0.0.1:4300"

func main() {
	server := strings.TrimSpace(os.Getenv("AUTONOMY_API_URL"))
	if server == "" {
		server = defaultServer
	}
	var agentID int64
	flag.StringVar(&server, "server", server, "Autonomy HTTP base URL")
	flag.Int64Var(&agentID, "agent", 0, "agent id (required)")
	flag.Parse()
	if agentID == 0 {
		fmt.Fprintln(os.Stderr, "usage: agentruntime -agent <id>")
		os.Exit(2)
	}
	url := strings.TrimRight(server, "/") + "/api/agents/" + fmt.Sprintf("%d", agentID) + "/runtime"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "%s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		fmt.Println(string(body))
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
