package autonomy_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src"
)

func TestLLMReasonerLive(t *testing.T) {
	if os.Getenv("CURSOR_LIVE") != "1" {
		t.Skip("set CURSOR_LIVE=1 to run")
	}
	if os.Getenv("CURSOR_API_KEY") == "" {
		t.Skip("CURSOR_API_KEY missing")
	}
	r := autonomy.NewLLMReasoner("composer-2")
	task := &autonomy.Task{
		ID: "live", Goal: "acknowledge", Description: "smoke",
		Target: "demo", Contract: autonomy.Contract{ExpectedState: "ok"},
	}
	done := make(chan struct{})
	var out autonomy.ReasoningResult
	var err error
	go func() {
		out, err = r.Reason(autonomy.DecisionContext{Task: task}, autonomy.ReasoningInput{Text: "Reply with the token HELLO_AUTONOMY and nothing else."})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("timeout")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Decision.Reason, "HELLO_AUTONOMY") && out.Decision.Reason == "" {
		t.Fatalf("empty or unexpected reason: %q", out.Decision.Reason)
	}
	t.Logf("reason=%q", out.Decision.Reason)
}
