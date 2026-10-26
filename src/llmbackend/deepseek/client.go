package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is DeepSeek's own endpoint; an account's BaseURL replaces it (a proxy, a
// compatible gateway, a test server).
const DefaultBaseURL = "https://api.deepseek.com"

// message is one Chat Completions message, in the shape DeepSeek takes and the shape the
// session stores. ReasoningContent is only sent back inside the turn that produced it
// (DeepSeek's thinking mode wants it during a tool loop, and refuses it afterwards).
type message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolSpec struct {
	Type     string       `json:"type"`
	Function functionSpec `json:"function"`
}

type functionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []message      `json:"messages"`
	Tools         []toolSpec     `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions map[string]any `json:"stream_options,omitempty"`
}

// chunk is one streamed `data:` line of a chat completion.
type chunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []chunkChoice  `json:"choices"`
	Usage   map[string]any `json:"usage"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []toolCallDelta `json:"tool_calls"`
}

type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// completion is what one streamed request added up to.
type completion struct {
	ID           string
	Model        string
	Message      message
	FinishReason string
	Usage        map[string]any
}

// client talks to one account's DeepSeek endpoint.
type client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newClient(baseURL, apiKey string) *client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	// No overall timeout: a long answer streams for minutes. The run's idle watchdog
	// (llmrun) cancels the context when the stream goes quiet.
	return &client{baseURL: base, apiKey: apiKey, http: &http.Client{Transport: http.DefaultTransport}}
}

func (c *client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("deepseek: %s %s: HTTP %d: %s", method, path, resp.StatusCode, apiError(raw))
	}
	return resp, nil
}

// apiError is the message of an OpenAI-style error body, or the body itself.
func apiError(raw []byte) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return strings.TrimSpace(string(raw))
}

// models lists the models the account can use (GET /models): the cheap credential check.
func (c *client) models(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("deepseek: decode models: %w", err)
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// stream runs one streamed chat completion and adds it up. Every native event it sees —
// a reasoning or text delta, a tool call fragment, usage, the finish — goes to onNative as
// it arrives; touch is called on every line (keep-alive comments included), so a quiet
// model thinking for a long time is not mistaken for a dead stream.
func (c *client) stream(ctx context.Context, req chatRequest, onNative func(map[string]any), touch func()) (completion, error) {
	req.Stream = true
	req.StreamOptions = map[string]any{"include_usage": true}
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", req)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()
	stop := context.AfterFunc(ctx, func() { _ = resp.Body.Close() })
	defer stop()

	var out completion
	out.Message.Role = "assistant"
	var content, reasoning strings.Builder
	var calls []toolCall
	done := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		touch()
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue // blank separators and ": keep-alive" comments
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return out, fmt.Errorf("deepseek: invalid stream chunk: %w", err)
		}
		out.ID = firstNonEmpty(out.ID, ch.ID)
		out.Model = firstNonEmpty(out.Model, ch.Model)
		for _, choice := range ch.Choices {
			d := choice.Delta
			if d.ReasoningContent != "" {
				reasoning.WriteString(d.ReasoningContent)
				onNative(map[string]any{"type": "reasoning_delta", "text": d.ReasoningContent})
			}
			if d.Content != "" {
				content.WriteString(d.Content)
				onNative(map[string]any{"type": "content_delta", "text": d.Content})
			}
			for _, tc := range d.ToolCalls {
				for len(calls) <= tc.Index {
					calls = append(calls, toolCall{Type: "function"})
				}
				call := &calls[tc.Index]
				call.ID = firstNonEmpty(call.ID, tc.ID)
				call.Function.Name += tc.Function.Name
				call.Function.Arguments += tc.Function.Arguments
				onNative(map[string]any{"type": "tool_call_delta", "index": tc.Index, "id": call.ID,
					"name": call.Function.Name, "arguments": tc.Function.Arguments})
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				out.FinishReason = *choice.FinishReason
				onNative(map[string]any{"type": "finish", "finish_reason": out.FinishReason})
			}
		}
		if len(ch.Usage) > 0 {
			out.Usage = ch.Usage
			onNative(map[string]any{"type": "usage", "usage": ch.Usage})
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("deepseek: read stream: %w", err)
	}
	if !done && out.FinishReason == "" {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("deepseek: stream ended without a finish")
	}
	out.Message.Content = content.String()
	out.Message.ReasoningContent = reasoning.String()
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	out.Message.ToolCalls = calls
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
