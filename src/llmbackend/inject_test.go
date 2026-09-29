package llmbackend

import "testing"

func TestSystemInjectDefaultsToFirstTurn(t *testing.T) {
	if got := SystemInjectFor(Backend("no-such-harness")); got != SystemInjectFirstTurn {
		t.Fatalf("unknown harness inject=%q, want first_turn", got)
	}
	if PlacesSystemAtSession(Backend("no-such-harness")) {
		t.Fatal("an unknown harness must not claim session-create injection")
	}
}
