package deepseek

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// fakeDeepSeek is an httptest stand-in for api.deepseek.com: each POST /chat/completions
// answers with the next scripted SSE stream and records the request it got.
type fakeDeepSeek struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	replies  [][]string // each reply: the data: payloads, in order
	requests []chatRequest
	auth     []string
}

func newFake(t *testing.T, replies ...[]string) *fakeDeepSeek {
	t.Helper()
	f := &fakeDeepSeek{t: t, replies: replies}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	t.Setenv("AUTONOMY_DEEPSEEK_SESSIONS_DIR", t.TempDir())
	return f
}

func (f *fakeDeepSeek) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/models":
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Authentication Fails"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-v4-pro"},{"id":"deepseek-chat"}]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/chat/completions":
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("fake: bad request body: %v", err)
		}
		f.requests = append(f.requests, req)
		if len(f.replies) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"no scripted reply left"}}`)
			return
		}
		reply := f.replies[0]
		f.replies = f.replies[1:]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		for _, data := range reply {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeDeepSeek) request(i int) chatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		f.t.Fatalf("fake got %d requests, want request %d", len(f.requests), i)
	}
	return f.requests[i]
}

// Chunk builders, in DeepSeek's stream shape.
func delta(field, text string) string {
	raw, _ := json.Marshal(map[string]any{"id": "cmpl-1", "model": "deepseek-v4-pro",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{field: text}}}})
	return string(raw)
}

func toolDelta(index int, id, name, args string) string {
	call := map[string]any{"index": index, "function": map[string]any{"arguments": args}}
	if id != "" {
		call["id"], call["type"] = id, "function"
		call["function"].(map[string]any)["name"] = name
	}
	raw, _ := json.Marshal(map[string]any{"id": "cmpl-1",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}}}})
	return string(raw)
}

func finish(reason string) string {
	return fmt.Sprintf(`{"id":"cmpl-1","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, reason)
}

func usageChunk(in, out int) string {
	return fmt.Sprintf(`{"id":"cmpl-1","choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_cache_hit_tokens":1,"completion_tokens_details":{"reasoning_tokens":2}}}`, in, out, in+out)
}

func (f *fakeDeepSeek) host(workspace string) *probeHost {
	return &probeHost{facts: llmbackend.Facts{Backend: llmbackend.Deepseek, Workspace: workspace,
		Creds: llmbackend.Creds{APIKey: "good-key", BaseURL: f.srv.URL}}}
}

// kinds lists the neutral kinds of the events a sink got.
func kinds(events []llmbackend.Event) string {
	var out []string
	for _, ev := range events {
		out = append(out, string(ev.Kind))
	}
	return strings.Join(out, ",")
}
