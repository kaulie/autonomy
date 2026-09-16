package autonomy

import (
	"encoding/json"
	"strings"
	"testing"
)

// A step input is a value or a binding, and nothing else: the two shapes a plan may
// write, and the two sources a binding may name. Everything here is about being
// loud: a binding the runtime cannot read must fail the plan, not travel as text.

// A step input is a value or a binding. Any scalar is a value — a capability's inputs
// are strings, and a model that writes `"timeout": 300` means the value, not a
// mistake — while an object with neither a source nor a value is the shape a broken
// binding has, and that must not travel as text.
func TestStepInputReadsValuesAndBindings(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    StepInput
		back    string // what the plan row keeps; empty means "the same JSON"
		wantErr string
	}{
		{name: "a plain value", raw: `"squash"`, want: StepInput{Literal: "squash"}},
		{name: "an empty value", raw: `""`, want: StepInput{Literal: ""}},
		{name: "a number", raw: `300`, want: StepInput{Literal: "300"}, back: `"300"`},
		{name: "a fractional number", raw: `1.5`, want: StepInput{Literal: "1.5"}, back: `"1.5"`},
		{name: "a negative number", raw: `-40`, want: StepInput{Literal: "-40"}, back: `"-40"`},
		{name: "a boolean", raw: `true`, want: StepInput{Literal: "true"}, back: `"true"`},
		{name: "null", raw: `null`, want: StepInput{}, back: `""`},
		{name: "a value written as an object", raw: `{"value":300}`, want: StepInput{Literal: "300"}, back: `"300"`},
		{name: "a value written as an object, as text", raw: `{"value":"squash"}`, want: StepInput{Literal: "squash"}, back: `"squash"`},
		{name: "a step output", raw: `{"source":"step:build.output.artifact_version"}`, want: StepInput{Source: "step:build.output.artifact_version"}},
		{name: "a world value", raw: `{"source":"world_model:asset.repo.state"}`, want: StepInput{Source: "world_model:asset.repo.state"}},
		{name: "a binding with extra keys", raw: `{"source":"step:build.output.x","note":"read it"}`, want: StepInput{Source: "step:build.output.x"}, back: `{"source":"step:build.output.x"}`},
		{name: "an object that names neither", raw: `{"spec":"x"}`, wantErr: "names neither a source nor a value"},
		{name: "a source that is not a string", raw: `{"source":300}`, wantErr: `"source" must be a string, not a number`},
		{name: "an empty source", raw: `{"source":"   "}`, wantErr: "names no source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var in StepInput
			err := json.Unmarshal([]byte(tc.raw), &in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if in != tc.want {
				t.Fatalf("input=%+v, want %+v", in, tc.want)
			}
			// What the plan row keeps: the same JSON for a binding, and the string the
			// capability is called with for a scalar (the reply verbatim is in
			// reason_turns.raw_output — src/execution-step.md).
			want := tc.back
			if want == "" {
				want = tc.raw
			}
			back, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(back) != want {
				t.Fatalf("round trip=%s, want %s", back, want)
			}
		})
	}
}

func TestInputSourceReadsTheTwoForms(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		wantErr bool
	}{
		{name: "a step output", source: "step:build.output.artifact_version"},
		{name: "a step name with - and _", source: "step:code_edit-1.output.pr_url"},
		{name: "a world state", source: "world_model:asset.repo.state"},
		{name: "a world kind", source: "world_model:asset.repo.kind"},
		{name: "an asset id with dots", source: "world_model:asset.github.com/kaulie.state"},
		{name: "no prefix", source: "build.output.artifact_version", wantErr: true},
		{name: "a dotted step name", source: "step:build.v1.output.version", wantErr: true},
		{name: "no output", source: "step:build.artifact_version", wantErr: true},
		{name: "no key", source: "step:build.output.", wantErr: true},
		{name: "a key with a dot", source: "step:build.output.service.id", wantErr: true},
		{name: "a world field the model has not", source: "world_model:asset.repo.owner", wantErr: true},
		{name: "a world root the model has not", source: "world_model:service.repo.state", wantErr: true},
		{name: "a bare world path", source: "world_model:asset.repo", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseInputSource(tc.source)
			if tc.wantErr && err == nil {
				t.Fatalf("parseInputSource(%q) accepted it", tc.source)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("parseInputSource(%q): %v", tc.source, err)
			}
			if err != nil && !strings.Contains(err.Error(), "cannot read an input source") {
				t.Fatalf("err=%v, want the two forms named", err)
			}
		})
	}
}
