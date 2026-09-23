package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
)

// A turn cut off by the model's output limit is a failed run — but a failed run
// of a session that is still perfectly usable. These tests pin the two halves of
// the runtime's answer: recognizing that failure, and spending a bounded retry on
// the same session instead of losing the whole delegation to one over-long turn.

func TestIsTruncatedTurnMatchesOnlyTheOutputLimitFailure(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"cline wait: cline run run_failed: Model reached the maximum output token limit before completing the turn", true},
		{"Model reached the maximum output token limit before completing the turn", true},
		{"cline run max_tokens: max output token limit reached", true},
		{"the request exceeds the model's maximum output tokens", true},
		{"cline wait: provider_error: provider exploded", false},
		{"empty model response (status=finished msg=Insufficient Balance)", false},
		{"run idle for 3m0s: no provider activity", false},
	}
	for _, tc := range cases {
		if got := isTruncatedTurn(errors.New(tc.msg)); got != tc.want {
			t.Errorf("isTruncatedTurn(%q)=%v want %v", tc.msg, got, tc.want)
		}
	}
	if isTruncatedTurn(nil) {
		t.Error("isTruncatedTurn(nil)=true, want false")
	}
}

func TestTurnRetryBudgetFromEnv(t *testing.T) {
	t.Setenv("AUTONOMY_LLM_TURN_RETRIES", "")
	if got := turnRetryBudget(); got != DefaultTurnRetries {
		t.Errorf("unset → %d want the default %d", got, DefaultTurnRetries)
	}
	t.Setenv("AUTONOMY_LLM_TURN_RETRIES", "3")
	if got := turnRetryBudget(); got != 3 {
		t.Errorf("3 → %d", got)
	}
	// 0 is a choice (retry off), not a mistake to be overridden.
	t.Setenv("AUTONOMY_LLM_TURN_RETRIES", "0")
	if got := turnRetryBudget(); got != 0 {
		t.Errorf("0 → %d want no retries", got)
	}
	for _, bad := range []string{"-1", "many"} {
		t.Setenv("AUTONOMY_LLM_TURN_RETRIES", bad)
		if got := turnRetryBudget(); got != DefaultTurnRetries {
			t.Errorf("%q → %d want the default %d", bad, got, DefaultTurnRetries)
		}
	}
}

// TestTurnTruncatedPromptIsThePolicyFile: the reminder is a repository file
// rendered for the worker it is addressed to, like every other prompt here.
func TestTurnTruncatedPromptIsThePolicyFile(t *testing.T) {
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	got, err := turnTruncatedPrompt(map[string]string{"{{WORKSPACE}}": "/sandbox/agent-1/"})
	if err != nil {
		t.Fatalf("turn truncated prompt: %v", err)
	}
	for _, want := range []string{"/sandbox/agent-1/", "cut off", "Continue from where your last completed turn left off"} {
		if !strings.Contains(got, want) {
			t.Errorf("reminder does not mention %q:\n%s", want, got)
		}
	}
	// Every placeholder is rendered — a frame value nobody filled reads as the
	// gap the runtime left, never as a raw {{NAME}} reaching the model.
	if strings.Contains(got, "{{") {
		t.Errorf("reminder still carries a placeholder:\n%s", got)
	}
}

// TestTurnTruncatedPromptNeedsItsPolicyFile: no reminder, no retry — the caller
// keeps the original failure rather than sending a prompt that says nothing.
func TestTurnTruncatedPromptNeedsItsPolicyFile(t *testing.T) {
	t.Setenv("PROJECT_ROOT", t.TempDir())
	if _, err := turnTruncatedPrompt(nil); err == nil {
		t.Fatal("expected an error when the policy file is missing")
	}
	t.Setenv("PROJECT_ROOT", "")
	if _, err := turnTruncatedPrompt(nil); err == nil {
		t.Fatal("expected an error without PROJECT_ROOT")
	}
}

// TestATruncatedTurnIsRetriedOnTheSameSession: the run that was cut off is
// recorded as the failure it was, and the retry is recorded as its own turn whose
// input is the reminder — on the same session, so the work already done is not
// thrown away.
func TestATruncatedTurnIsRetriedOnTheSameSession(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	t.Setenv("AUTONOMY_LLM_TURN_RETRIES", "") // one retry, the default
	store := executionTestStore(t)

	rt := NewRuntime(NewAgentFactory())
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{Purpose: "code_edit", TaskID: "task-9"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()

	text, err := sess.Prompt(context.Background(), "truncate my turn")
	if err != nil {
		t.Fatalf("a truncated turn should be retried, not reported: %v", err)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("the retry answered nothing")
	}

	rows, err := store.RawDB().Query(`SELECT status, error_message, input FROM reason_turns WHERE task_id = 'task-9' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type turn struct{ status, errMsg, input string }
	var turns []turn
	for rows.Next() {
		var one turn
		if err := rows.Scan(&one.status, &one.errMsg, &one.input); err != nil {
			t.Fatal(err)
		}
		turns = append(turns, one)
	}
	if len(turns) != 2 {
		t.Fatalf("recorded %d turns, want the truncated one and its retry: %+v", len(turns), turns)
	}
	if turns[0].status != string(llmbackend.StatusError) || !strings.Contains(turns[0].errMsg, "maximum output token limit") {
		t.Errorf("first turn = %+v, want the failure the provider reported", turns[0])
	}
	if turns[0].input != "truncate my turn" {
		t.Errorf("first turn input=%q, want the original prompt", turns[0].input)
	}
	if !strings.Contains(turns[1].input, "Your turn was cut off") {
		t.Errorf("retry input=%q, want the continuation reminder", turns[1].input)
	}
	if turns[1].status == string(llmbackend.StatusError) {
		t.Errorf("retry turn = %+v, want a successful run", turns[1])
	}
}

// TestATruncatedTurnIsNotRetriedForever: the budget is a budget. With the retry
// turned off the failure is reported exactly as before.
func TestATruncatedTurnIsNotRetriedForever(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	t.Setenv("AUTONOMY_LLM_TURN_RETRIES", "0")
	store := executionTestStore(t)

	rt := NewRuntime(NewAgentFactory())
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{Purpose: "code_edit", TaskID: "task-9"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()

	if _, err := sess.Prompt(context.Background(), "truncate my turn"); err == nil {
		t.Fatal("expected the truncated run to surface with retries off")
	}
	var turns int
	if err := store.RawDB().QueryRow(`SELECT COUNT(*) FROM reason_turns WHERE task_id = 'task-9'`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 1 {
		t.Fatalf("recorded %d turns, want only the one that ran", turns)
	}
}

// TestOtherFailuresAreNotRetried: a provider error is not a truncated turn. A run
// that failed for another reason is reported as it always was — one turn, no
// reminder.
func TestOtherFailuresAreNotRetried(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	t.Setenv("AUTONOMY_LLM_TURN_RETRIES", "")
	store := executionTestStore(t)

	rt := NewRuntime(NewAgentFactory())
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{Purpose: "code_edit", TaskID: "task-9"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()

	_, err = sess.Prompt(context.Background(), "fail me")
	if err == nil || !strings.Contains(err.Error(), "provider exploded") {
		t.Fatalf("err=%v, want the provider's own failure", err)
	}
	var turns int
	if err := store.RawDB().QueryRow(`SELECT COUNT(*) FROM reason_turns WHERE task_id = 'task-9'`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 1 {
		t.Fatalf("recorded %d turns, want only the one that ran", turns)
	}
}
