// Package fakebridge is a test double for the Cline bridge: it speaks the same
// NDJSON stdio protocol as src/clinesdk/bridge/bridge.mjs without needing Node,
// the Cline SDK or a provider account, so client tests stay hermetic.
//
// A test process re-executes its own binary with TestHelperProcess-style
// plumbing: the spawning test sets EnvVar=1 and calls Manager(os.Args[0]).
package fakebridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// EnvVar marks a re-executed test binary as the fake bridge.
const EnvVar = "CLINE_FAKE_BRIDGE"

// Enabled reports whether this process was started as the fake bridge.
func Enabled() bool { return os.Getenv(EnvVar) == "1" }

// decisionAnswer is one canned reply a test drives the loop with: the phrase its task
// description carries, and the whole decision JSON the fake planner answers with.
type decisionAnswer struct {
	phrase string
	answer string
}

// decisionAnswers are the concluding decisions a test drives the loop with, each a
// whole answer: the first three are valid (`done` carries the evidence that proves it,
// `blocked` / `need_input` say what is missing) and a run must stop on any of them.
// The last one is the opposite case — a `done` that proves nothing, which breaks
// `done.evidence_present` (src/decision_rules.go) and must not end a run at all.
//
// A slice, not a map: which phrase matched must not depend on iteration order, since
// one phrase may be a substring of another.
var decisionAnswers = []decisionAnswer{
	{"answer done", `{"type":"done","reason":"the fake planner found the goal satisfied","evidence":[` +
		`{"id":"E1","source":"observation","reference":"asset-1","fact":"the asset is healthy"}]}`},
	{"answer blocked", `{"type":"blocked","reason":"nothing here can do this",` +
		`"need":{"type":"capability","description":"no capability in this runtime can change that service"}}`},
	{"answer need_input", `{"type":"need_input","reason":"the target is ambiguous",` +
		`"need":{"type":"decision","description":"which environment should this deploy to?"}}`},
	{"unproven answer", `{"type":"done","reason":"the fake planner says it is done, and proves nothing"}`},
}

// cannedDecisionAnswer is the reply for one of the decision phrases, or "" when the
// prompt asks for none of them.
func cannedDecisionAnswer(prompt string) string {
	for _, canned := range decisionAnswers {
		if strings.Contains(prompt, canned.phrase) {
			return canned.answer
		}
	}
	return ""
}

// Manager returns a BridgeManager that runs this test binary as the bridge.
func Manager(self string) *clinesdk.BridgeManager {
	return &clinesdk.BridgeManager{
		Command: []string{self, "-test.run=TestFakeBridgeProcess"},
		Env:     []string{EnvVar + "=1"},
	}
}

// Main serves the bridge protocol on stdin/stdout until EOF or "shutdown".
// Behavior is deterministic so assertions can be exact:
//
//	first send on a session  -> reasoning + text "pong", usage in 11/out 2, cost 0.0001
//	later sends              -> tool call + text "tool said hi", delta usage
//	prompt containing "truncate my turn" -> the provider cuts the turn off at the
//	                            output-token limit: the run fails with the SDK's
//	                            own message (the runtime may retry that turn)
//	prompt containing "answer done" / "answer blocked" / "answer need_input" -> that
//	                            concluding decision, validly written (evidence for `done`,
//	                            a described need for the other two), and no steps
//	prompt containing "unproven answer" -> a `done` that proves nothing, which the
//	                            runtime refuses (done.evidence_present)
//	prompt containing "never answer" -> never answers (the client must time out)
//	prompt containing "out of balance" -> finished with no text, and the reason only
//	                            in the run result (an exhausted account)
//	prompt containing "fail me" -> provider error
//
// Triggers are phrases, not single words: a real prompt carries the agent policy,
// so a bare "hang" would match its "change" and a busy prompt would look stuck.
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
	event := func(id, agentID, sessionID string, inner map[string]any) {
		write(map[string]any{
			"type": "event", "requestId": id, "agentId": agentID, "sessionId": sessionID,
			"event": map[string]any{"type": "agent_event", "payload": map[string]any{
				"sessionId": sessionID, "event": inner,
			}},
		})
	}
	write(map[string]any{"type": "ready", "protocol": clinesdk.Protocol, "pid": os.Getpid(), "node": "fake", "sdk": "0.0.82"})

	sessionForAgent := map[string]string{}
	modeForAgent := map[string]string{}
	cumulativeInput := int64(0)

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
			result(req.ID, map[string]any{
				"protocol": clinesdk.Protocol, "node": "fake", "sdk": "0.0.82", "pid": os.Getpid(),
				"providers": []string{"deepseek", "anthropic"},
			})
		case "models":
			result(req.ID, map[string]any{
				"models": []map[string]string{{"id": "deepseek-v4-pro", "displayName": "DeepSeek V4 Pro"}},
			})
		case "createAgent":
			cumulativeInput = 0
			agentID := fmt.Sprintf("cls_%d", len(modeForAgent)+1)
			mode := fmt.Sprint(req.Params["mode"])
			modeForAgent[agentID] = mode
			result(req.ID, map[string]any{
				"agentId": agentID, "mode": mode, "providerId": req.Params["providerId"],
				"modelId": req.Params["modelId"], "cwd": req.Params["cwd"],
			})
		case "send":
			agentID := fmt.Sprint(req.Params["agentId"])
			prompt := fmt.Sprint(req.Params["prompt"])
			sessionID, started := sessionForAgent[agentID]
			if !started {
				sessionID = "cls-session-" + agentID
				sessionForAgent[agentID] = sessionID
			}
			mode := modeForAgent[agentID]
			write(map[string]any{
				"type": "event", "requestId": req.ID, "agentId": agentID, "sessionId": sessionID,
				"event": map[string]any{"type": "status", "payload": map[string]any{
					"sessionId": sessionID, "status": "running",
				}},
			})
			switch {
			case strings.Contains(prompt, "give me a plan"):
				// A valid AGENT_V2 answer, so a test can drive the real loop — plan,
				// execution records and all — against the fake bridge instead of only
				// asserting what a parsed decision would do.
				text := `{"type":"plan","reason":"the fake planner decided","evidence":[{"id":"E1","source":"goal","reference":"the goal","fact":"the asset must change"}],"plan":{"steps":[` +
					`{"capability":"asset.change","input":{"target":"asset-1"},"expected_effect":"the asset's state changes","evidence_refs":["E1"]}]},"need":{}}`
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_start", "contentType": "text", "text": text, "accumulated": text,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_end", "contentType": "text", "text": text,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "done", "reason": "completed", "text": text, "iterations": 1,
				})
				cumulativeInput += 11
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "finished",
					"text": text, "finishReason": "completed", "usageSource": "run",
					"usage": map[string]any{"inputTokens": 11, "outputTokens": 2, "totalTokens": 13, "costUsd": 0.0001},
				})
			case cannedDecisionAnswer(prompt) != "":
				// A canned decision, written the way the planner writes one: `done` with the
				// evidence that proves it, `blocked` / `need_input` with the need that says what
				// is missing, or — for the "unproven answer" phrase — a `done` the runtime has to
				// refuse. None of them executes anything, and a valid one is what a run stops on
				// (Decision.Concludes).
				text := cannedDecisionAnswer(prompt)
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_start", "contentType": "text", "text": text, "accumulated": text,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_end", "contentType": "text", "text": text,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "done", "reason": "completed", "text": text, "iterations": 1,
				})
				cumulativeInput += 11
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "finished",
					"text": text, "finishReason": "completed", "usageSource": "run",
					"usage": map[string]any{"inputTokens": 11, "outputTokens": 2, "totalTokens": 13, "costUsd": 0.0001},
				})
			case strings.Contains(prompt, "never answer"):
				// Never answers: the client must abort on its own context.
			case strings.Contains(prompt, "stream slowly"):
				// Keeps producing events well past any short idle budget: this is
				// how a busy long run must survive (the budget bounds silence).
				for i := 0; i < 12; i++ {
					event(req.ID, agentID, sessionID, map[string]any{
						"type": "content_start", "contentType": "text", "text": "tick ", "accumulated": "tick ",
					})
					time.Sleep(60 * time.Millisecond)
				}
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_end", "contentType": "text", "text": "slow but alive",
				})
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "finished",
					"text": "slow but alive", "finishReason": "completed", "usageSource": "run",
					"usage": map[string]any{"inputTokens": 7, "outputTokens": 3, "totalTokens": 10, "costUsd": 0.00001},
				})
			case strings.Contains(prompt, "out of balance"):
				// A provider that ends the run as "finished" while having answered
				// nothing at all: an exhausted account does exactly this, after a long
				// think. The client must not call that a finished run.
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_start", "contentType": "reasoning", "reasoning": "thinking", "redacted": false,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "error", "error": map[string]any{}, "errorClass": "unknown",
					"iteration": 2, "recoverable": false,
				})
				// The reason the run result carries, re-emitted inside the request
				// window the way bridge.mjs does (runResultErrorEvent): an event that
				// arrives after that window closes is dropped by the client, so the
				// failure would exist only in the result.
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "error", "error": map[string]any{"message": "Insufficient Balance"},
					"source": "run_result", "recoverable": false,
				})
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "finished",
					"text": "", "finishReason": "completed",
					"lastError": map[string]any{"code": "", "message": "Insufficient Balance"},
				})
			case strings.Contains(prompt, "fail me"):
				// The real bridge forwarded a structured error here, which broke the client;
				// keep that shape so the tolerant decoder stays covered end to end.
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "error", "error": map[string]any{"code": "provider_error", "message": "provider exploded"},
				})
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "error",
					"text": "", "finishReason": "error",
					"lastError": map[string]any{"code": "provider_error", "message": "provider exploded"},
				})
			case strings.Contains(prompt, "truncate my turn"):
				// The turn was cut off at the model's output limit before it finished:
				// the SDK raises the turn as a failed run (no tool call, finish reason
				// max-tokens) with this exact message, while the session stays alive.
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "error",
					"error": map[string]any{
						"code":    "run_failed",
						"message": "Model reached the maximum output token limit before completing the turn",
					},
				})
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "error",
					"text": "", "finishReason": "max-tokens",
					"lastError": map[string]any{
						"code":    "run_failed",
						"message": "Model reached the maximum output token limit before completing the turn",
					},
				})
			case started:
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_start", "contentType": "tool", "toolName": "run_commands",
					"toolCallId": "call-1", "input": map[string]any{"commands": []string{"echo hi"}},
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_update", "contentType": "tool", "toolName": "run_commands",
					"toolCallId": "call-1", "update": map[string]any{"stream": "stdout", "chunk": "hi\n"},
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_end", "contentType": "tool", "toolName": "run_commands",
					"toolCallId": "call-1", "output": "hi\n", "durationMs": 7,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_start", "contentType": "text", "text": "tool said ", "accumulated": "tool said ",
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_end", "contentType": "text", "text": "tool said hi",
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "done", "reason": "completed", "text": "tool said hi", "iterations": 2,
				})
				cumulativeInput += 15
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "finished",
					"text": "tool said hi", "finishReason": "completed", "usageSource": "accumulated_delta",
					"usage": map[string]any{"inputTokens": 5, "outputTokens": 4, "totalTokens": 9, "costUsd": 0.00004},
				})
			default:
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_start", "contentType": "reasoning", "reasoning": "thinking", "redacted": false,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_end", "contentType": "reasoning", "reasoning": "thought it through",
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_start", "contentType": "text", "text": "p", "accumulated": "p",
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "content_end", "contentType": "text", "text": "pong",
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "usage", "inputTokens": 11, "outputTokens": 2, "cost": 0.0001,
					"totalInputTokens": 11, "totalOutputTokens": 2, "totalCost": 0.0001,
				})
				event(req.ID, agentID, sessionID, map[string]any{
					"type": "done", "reason": "completed", "text": "pong", "iterations": 1,
				})
				cumulativeInput += 11
				result(req.ID, map[string]any{
					"agentId": agentID, "sessionId": sessionID, "mode": mode, "status": "finished",
					"text": "pong", "finishReason": "completed", "usageSource": "run",
					"usage": map[string]any{"inputTokens": 11, "outputTokens": 2, "totalTokens": 13, "costUsd": 0.0001},
				})
			}
		case "usage":
			result(req.ID, map[string]any{
				"usage": map[string]any{"inputTokens": cumulativeInput, "outputTokens": 6, "totalTokens": cumulativeInput + 6},
			})
		case "stop", "close":
			agentID := fmt.Sprint(req.Params["agentId"])
			delete(sessionForAgent, agentID)
			result(req.ID, map[string]any{"agentId": agentID, "stopped": true, "forgotten": req.Cmd == "close"})
		case "shutdown":
			result(req.ID, map[string]any{"shutdown": true})
			return
		default:
			write(map[string]any{"type": "result", "id": req.ID, "ok": false,
				"error": map[string]any{"code": "unknown_command", "message": req.Cmd}})
		}
	}
}
