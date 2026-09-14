package autonomy

import "testing"

func TestFormatLLMMessageRendersEachRole(t *testing.T) {
	t.Setenv("AUTONOMY_LLM_TRACE_MAX", "")

	cases := []struct {
		name string
		msg  LLMMessage
		want string
	}{
		{
			name: "user input",
			msg:  LLMMessage{Seq: 0, Role: LLMMessageRoleUser, Content: "list\nthe   bridge dir"},
			want: "[autonomy] llm seq=0 user: list the bridge dir",
		},
		{
			name: "thinking block with its duration",
			msg: LLMMessage{Seq: 1, Role: LLMMessageRoleThinking, Content: "I should look at the code first",
				NormalizedContent: `{"duration_ms":1834}`},
			want: "[autonomy] llm seq=1 thinking (1.834s): I should look at the code first",
		},
		{
			name: "thinking block without a duration",
			msg:  LLMMessage{Seq: 1, Role: LLMMessageRoleThinking, Content: "no timing here"},
			want: "[autonomy] llm seq=1 thinking: no timing here",
		},
		{
			name: "tool call with args next to its result",
			msg: LLMMessage{Seq: 2, Role: LLMMessageRoleTool, Content: `{"exit":0,"stdout":"a\n"}`,
				NormalizedContent: `{"name":"execute_command","call_id":"toolu_01","args":{"command":"ls"}}`},
			want: `[autonomy] llm seq=2 tool execute_command call=toolu_01 args={"command":"ls"} -> {"exit":0,"stdout":"a\n"}`,
		},
		{
			name: "tool call without args",
			msg: LLMMessage{Seq: 2, Role: LLMMessageRoleTool, Content: `"ok"`,
				NormalizedContent: `{"name":"read_file","call_id":"call-2","args":null}`},
			want: `[autonomy] llm seq=2 tool read_file call=call-2 -> "ok"`,
		},
		{
			name: "assistant return",
			msg:  LLMMessage{Seq: 3, Role: LLMMessageRoleAssistant, Content: "all set"},
			want: "[autonomy] llm seq=3 assistant: all set",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLLMMessage(tc.msg); got != tc.want {
				t.Fatalf("formatLLMMessage=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestCondenseLLMTextCutsOnRunes(t *testing.T) {
	if got := condenseLLMText("a\n\tb   c", 100); got != "a b c" {
		t.Fatalf("whitespace collapse=%q", got)
	}
	if got := condenseLLMText("思考一下这个问题", 4); got != "思考一下…(+4ch)" {
		t.Fatalf("rune cut=%q", got)
	}
}

func TestFormatLLMMessageRespectsTheWidthBudget(t *testing.T) {
	t.Setenv("AUTONOMY_LLM_TRACE_MAX", "4")
	got := formatLLMMessage(LLMMessage{Seq: 1, Role: LLMMessageRoleThinking, Content: "abcdefgh"})
	if want := "[autonomy] llm seq=1 thinking: abcd…(+4ch)"; got != want {
		t.Fatalf("formatLLMMessage=%q, want %q", got, want)
	}
}

func TestLLMMessageLogSwitches(t *testing.T) {
	t.Setenv("AUTONOMY_LLM_TRACE", "0")
	if llmMessageLogEnabled() {
		t.Fatal("AUTONOMY_LLM_TRACE=0 must silence the message log")
	}
	t.Setenv("AUTONOMY_LLM_TRACE", "")
	if !llmMessageLogEnabled() {
		t.Fatal("the message log is on by default")
	}
	t.Setenv("AUTONOMY_LLM_TRACE_MAX", "42")
	if got := llmMessageLogMax(); got != 42 {
		t.Fatalf("AUTONOMY_LLM_TRACE_MAX=42 -> %d", got)
	}
	t.Setenv("AUTONOMY_LLM_TRACE_MAX", "nonsense")
	if got := llmMessageLogMax(); got != llmMessageLogDefaultMax {
		t.Fatalf("invalid AUTONOMY_LLM_TRACE_MAX -> %d, want the default %d", got, llmMessageLogDefaultMax)
	}
}
