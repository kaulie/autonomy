package bridgesdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testRun() *Run {
	return &Run{agent: &Agent{ID: "cls_1", SessionID: "cls-session-1"}, startedAt: time.Now()}
}

// TestDecodeResultToleratesStructuredError is the regression test for the real
// failure: the SDK reported its error as an object, the bridge passed it through
// and the client refused to decode the finished run.
func TestDecodeResultToleratesStructuredError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "error object with message",
			raw:  `{"status":"error","lastError":{"code":"provider_error","message":"provider exploded"}}`,
			want: "provider exploded",
		},
		{
			name: "error object with nested error",
			raw:  `{"status":"error","lastError":{"error":{"message":"nested boom"}}}`,
			want: "nested boom",
		},
		{
			name: "error object without message keys",
			raw:  `{"status":"error","lastError":{"foo":1}}`,
			want: `{"foo":1}`,
		},
		{
			name: "plain string still works",
			raw:  `{"status":"error","lastError":"plain boom"}`,
			want: "plain boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := testRun().decodeResult(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if res.Status != LLMStatusError {
				t.Fatalf("status=%q want error", res.Status)
			}
			if res.ErrorMessage != tc.want {
				t.Fatalf("error message=%q want %q", res.ErrorMessage, tc.want)
			}
		})
	}
}

func TestDecodeResultToleratesNonStringTextAndNumbers(t *testing.T) {
	res, err := testRun().decodeResult(json.RawMessage(`{
		"status":"finished",
		"text":{"blocks":[{"type":"text","text":"hi"}]},
		"mode":3,
		"usage":{"inputTokens":"1637","outputTokens":4,"costUsd":"0.000052113","reasoningTokens":null}
	}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != LLMStatusFinished {
		t.Fatalf("status=%q", res.Status)
	}
	if !strings.Contains(res.Text, `"hi"`) {
		t.Fatalf("text=%q want the compact JSON of the block", res.Text)
	}
	if res.Mode != "3" {
		t.Fatalf("mode=%q want \"3\"", res.Mode)
	}
	if res.Usage.InputTokens != 1637 || res.Usage.OutputTokens != 4 {
		t.Fatalf("usage=%+v want the numeric strings to parse", res.Usage)
	}
	if !res.Usage.HasCost || res.Usage.CostUSD < 0.00005 || res.Usage.CostUSD > 0.00006 {
		t.Fatalf("cost=%+v", res.Usage)
	}
	if res.Usage.ReasoningTokens != nil {
		t.Fatalf("reasoning tokens=%v want nil for a null value", *res.Usage.ReasoningTokens)
	}
}

func TestDecodeResultKeepsTheSessionItContinued(t *testing.T) {
	// The bridge reports the session a run continued (its first run on a session seeded
	// from an earlier one's transcript): the run's own record of being a continuation.
	res, err := testRun().decodeResult(json.RawMessage(`{
		"status":"finished","sessionId":"cls-new","resumedFrom":"cls-old","text":"pong"
	}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.SessionID != "cls-new" || res.ResumedFrom != "cls-old" {
		t.Fatalf("session=%q resumedFrom=%q, want cls-new from cls-old", res.SessionID, res.ResumedFrom)
	}
	// A run that started a session from nothing carries no resumedFrom at all.
	plain, err := testRun().decodeResult(json.RawMessage(`{"status":"finished","sessionId":"cls-fresh"}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if plain.ResumedFrom != "" {
		t.Fatalf("resumedFrom=%q, want empty", plain.ResumedFrom)
	}
}

func TestDecodeResultUnknownStatusBecomesFinished(t *testing.T) {
	res, err := testRun().decodeResult(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != LLMStatusFinished {
		t.Fatalf("status=%q want finished", res.Status)
	}
	if res.SessionID != "cls-session-1" {
		t.Fatalf("session=%q want the agent's session", res.SessionID)
	}
}

// TestTraceVerboseDefaultOff pins the shared AUTONOMY_LLM_DEBUG semantics: the
// Cursor client treats it as an opt-in, and so must this one (the first version
// had the default inverted and logged every event).
func TestTraceVerboseDefaultOff(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"verbose", true},
	} {
		t.Setenv("AUTONOMY_LLM_DEBUG", tc.env)
		if got := TraceVerbose(); got != tc.want {
			t.Fatalf("AUTONOMY_LLM_DEBUG=%q → %v want %v", tc.env, got, tc.want)
		}
	}
}
