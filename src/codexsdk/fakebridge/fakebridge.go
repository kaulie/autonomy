// Package fakebridge is a test double for the Codex bridge: it speaks the same NDJSON stdio
// protocol as src/codexsdk/bridge/bridge.mjs (and the shape of the Codex SDK's
// thread/turn/item events) without needing Node, the Codex CLI or an account, so the codex
// harness's tests stay hermetic.
//
// It is deliberately its own fake rather than a mode of src/bridgesdk/fakebridge: that one
// speaks the Cline bridge's wire shape (agent_event payloads), and faking one means
// reproducing one protocol faithfully.
//
// A test process re-executes its own binary: the spawning test sets EnvVar=1 and calls
// Command(os.Args[0]).
package fakebridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EnvVar marks a re-executed test binary as the fake codex bridge.
const EnvVar = "CODEX_FAKE_BRIDGE"

// Protocol is the wire shape this fake speaks.
const Protocol = "codex-bridge/1"

// Enabled reports whether this process was started as the fake bridge.
func Enabled() bool { return os.Getenv(EnvVar) == "1" }

// Command returns the argv and environment that make a test binary serve this protocol. It
// imports nothing, so any test package can use it.
func Command(self string) (argv, env []string) {
	return []string{self, "-test.run=TestFakeCodexBridgeProcess"}, []string{EnvVar + "=1"}
}

// Main serves the bridge protocol on stdin/stdout until EOF or "shutdown". Deterministic:
//
//	ping        -> protocol handshake
//	createAgent -> a thread handle, echoing resumeSessionId it was asked to continue
//	send        -> thread.started, turn.started, item.completed(agent_message "pong"),
//	               turn.completed(usage) and a result carrying the thread id + text
func Main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(out, "%s\n", b)
		out.Flush()
	}
	result := func(id string, payload map[string]any) {
		write(map[string]any{"type": "result", "id": id, "ok": true, "result": payload})
	}
	write(map[string]any{"type": "ready", "protocol": Protocol, "pid": os.Getpid(), "node": "fake", "sdk": "0.156.1"})

	threadForAgent := map[string]string{}
	sandboxForAgent := map[string]string{}
	threadSeq := 0

	for in.Scan() {
		var req struct {
			ID     string         `json:"id"`
			Cmd    string         `json:"cmd"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			continue
		}
		switch req.Cmd {
		case "ping":
			result(req.ID, map[string]any{"protocol": Protocol, "node": "fake", "sdk": "0.156.1", "pid": os.Getpid()})
		case "models":
			result(req.ID, map[string]any{"models": []map[string]string{}})
		case "createAgent":
			agentID := fmt.Sprintf("cdx_%d", len(sandboxForAgent)+1)
			mode := fmt.Sprint(req.Params["mode"])
			sandbox := "workspace-write"
			if mode == "plan" {
				sandbox = "read-only"
			}
			sandboxForAgent[agentID] = sandbox
			resume := strings.TrimSpace(fmt.Sprint(req.Params["resumeSessionId"]))
			if resume == "<nil>" {
				resume = ""
			}
			if resume != "" {
				// Resuming: the thread id is known from the start (the real bridge
				// reports the resumed thread immediately).
				threadForAgent[agentID] = resume
			}
			payload := map[string]any{
				"agentId": agentID, "mode": mode, "sandboxMode": sandbox,
				"cwd": req.Params["cwd"], "modelId": req.Params["modelId"], "providerId": "openai",
			}
			if resume != "" {
				payload["sessionId"] = resume
				payload["resumedFrom"] = resume
			} else {
				payload["sessionId"] = nil
			}
			result(req.ID, payload)
		case "send":
			agentID := fmt.Sprint(req.Params["agentId"])
			threadID, started := threadForAgent[agentID]
			if !started {
				// A Codex thread id exists only once a turn started (the real bridge
				// reports it with every run's result, and the client records it).
				threadSeq++
				threadID = fmt.Sprintf("thread-%d", threadSeq)
				threadForAgent[agentID] = threadID
			}
			emit := func(event map[string]any) {
				write(map[string]any{
					"type": "event", "requestId": req.ID, "agentId": agentID, "sessionId": threadID, "event": event,
				})
			}
			emit(map[string]any{"type": "thread.started", "thread_id": threadID})
			emit(map[string]any{"type": "turn.started"})
			emit(map[string]any{"type": "item.completed", "item": map[string]any{
				"id": "item-1", "type": "agent_message", "text": "pong",
			}})
			emit(map[string]any{"type": "turn.completed", "usage": map[string]any{
				"input_tokens": 11, "output_tokens": 2, "cached_input_tokens": 3, "total_tokens": 13,
			}})
			result(req.ID, map[string]any{
				"sessionId": threadID, "text": "pong", "status": "finished", "modelId": req.Params["modelId"],
				"providerId": "openai", "durationMs": 5,
				"usage": map[string]any{"input_tokens": 11, "output_tokens": 2, "cached_input_tokens": 3, "total_tokens": 13},
			})
		case "stop":
			result(req.ID, map[string]any{"stopped": false})
		case "close":
			result(req.ID, map[string]any{"closed": true})
		case "usage":
			result(req.ID, map[string]any{"usage": nil})
		case "shutdown":
			result(req.ID, map[string]any{"stopped": true})
			return
		default:
			write(map[string]any{"type": "result", "id": req.ID, "ok": false,
				"error": map[string]any{"code": "unknown_command", "message": req.Cmd}})
		}
	}
}
