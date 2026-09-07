package autonomy

import "testing"

func TestParseDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		text   string
		noop   bool
		reason string
	}{
		{
			name:   "change",
			text:   "ACTION: change\nREASON: move target to expected state",
			noop:   false,
			reason: "move target to expected state",
		},
		{
			name:   "noop",
			text:   "ACTION: noop\nREASON: wait",
			noop:   true,
			reason: "wait",
		},
		{
			name:   "default change",
			text:   "just free text without tags",
			noop:   false,
			reason: "just free text without tags",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason, action := parseDecision(tc.text)
			if reason != tc.reason {
				t.Fatalf("reason=%q want %q", reason, tc.reason)
			}
			_, isNoop := action.(NothingAction)
			if isNoop != tc.noop {
				t.Fatalf("noop=%v want %v", isNoop, tc.noop)
			}
		})
	}
}
