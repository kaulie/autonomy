package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The command is a client of the HTTP API, so what it is tested against is that
// API: an httptest server that answers the documented paths and records what it
// was asked. Nothing of the runtime is linked in here either.

func TestSubmitPostsTheDocumentedRequest(t *testing.T) {
	var got acceptRequest
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"task_id":"task-1","agent_id":10001,"status":"pending","message_id":7,"queued":0}`)
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := cli([]string{
		"-server", srv.URL, "-task", "task-1", "-wait=false",
		"-description", "把服务契约的前端入口放开",
		"-domain", "software_development", "-goal", "dev_feature",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if method != http.MethodPost || path != "/api/tasks" {
		t.Errorf("asked %s %s, want POST /api/tasks", method, path)
	}
	if got.TaskID != "task-1" || got.Description != "把服务契约的前端入口放开" ||
		got.Domain != "software_development" || got.GoalType != "dev_feature" {
		t.Errorf("request = %+v", got)
	}
	if got.ContextRef["project"] != "project-2" {
		t.Errorf("context_ref = %v, want the demo project", got.ContextRef)
	}
	if !strings.Contains(out.String(), "task task-1 agent 10001 status pending message 7 queued 0") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestWaitFollowsTheTaskAndItsConversation(t *testing.T) {
	var polls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks":
			_, _ = io.WriteString(w, `{"task_id":"task-1","agent_id":10001,"status":"pending","message_id":7}`)
		case r.URL.Path == "/api/tasks/task-1/agents/10001/events":
			switch atomic.AddInt64(&polls, 1) {
			case 1:
				// The priming read: the conversation the agent already had, and
				// its cursor. Following must not reprint it.
				if got := r.URL.Query().Get("last_synced_message_seq"); got != "0" {
					t.Errorf("prime cursor = %s, want 0", got)
				}
				_, _ = io.WriteString(w, `{"task_id":"task-1","agent_id":10001,"last_message_seq":4,"next_poll_after_seq":4,
					"events":[{"message_seq":4,"role":"assistant","content":"an earlier answer"}]}`)
			default:
				if got := r.URL.Query().Get("last_synced_message_seq"); got != "4" {
					t.Errorf("follow cursor = %s, want 4", got)
				}
				_, _ = io.WriteString(w, `{"task_id":"task-1","agent_id":10001,"last_message_seq":5,"next_poll_after_seq":5,
					"events":[{"message_seq":5,"role":"tool","content":"code_edit\n{\"goal\":\"…\"}"}]}`)
			}
		case r.URL.Path == "/api/tasks/task-1":
			_, _ = io.WriteString(w, `{"task_id":"task-1","status":"completed","agent_id":10001,
				"plans":[{"plan_id":1,"cycle":1,"decision_type":"execute","step_count":2,"executed":2,"outcome":"ok","steps":[]}]}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := cli([]string{"-server", srv.URL, "-task", "task-1", "-poll", "10ms"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	text := out.String()
	if strings.Contains(text, "an earlier answer") {
		t.Errorf("the conversation before the instruction was reprinted:\n%s", text)
	}
	for _, want := range []string{"[llm 5] tool code_edit", "[task task-1] status completed cycle=1 decision=execute steps=2/2 outcome=ok"} {
		if !strings.Contains(text, want) {
			t.Errorf("stdout missing %q:\n%s", want, text)
		}
	}
}

func TestAFailedRunIsANonZeroExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks":
			_, _ = io.WriteString(w, `{"task_id":"task-1","agent_id":10001,"status":"pending","message_id":7}`)
		case strings.HasPrefix(r.URL.Path, "/api/tasks/task-1/agents/"):
			_, _ = io.WriteString(w, `{"task_id":"task-1","agent_id":10001,"events":[],"next_poll_after_seq":0}`)
		default:
			_, _ = io.WriteString(w, `{"task_id":"task-1","status":"error","error":"decide: bridge is not attached","agent_id":10001,"plans":[]}`)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := cli([]string{"-server", srv.URL, "-task", "task-1", "-poll", "10ms"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr = %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "status error: decide: bridge is not attached") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestStopAndProgressAreTheTaskPaths(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"task_id":"task-28","status":"stopped"}`)
		case strings.HasSuffix(r.URL.Path, "/task-404"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"task not found"}`)
		case strings.HasSuffix(r.URL.Path, "/task-failed"):
			_, _ = io.WriteString(w, `{"task_id":"task-failed","status":"error","error":"decide: no bridge","agent_id":10000,"plans":[]}`)
		default:
			_, _ = io.WriteString(w, `{"task_id":"task-28","status":"running","agent_id":10000,"plans":[]}`)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	if code := cli([]string{"-server", srv.URL, "-stop"}, &out, &errOut); code != 0 {
		t.Fatalf("-stop exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "task task-28 stopped") {
		t.Errorf("-stop stdout = %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := cli([]string{"-server", srv.URL, "-progress"}, &out, &errOut); code != 0 {
		t.Fatalf("-progress exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "task task-28  status running  agent 10000") {
		t.Errorf("-progress stdout = %q", out.String())
	}

	// -progress exits by the status it read: a failed task is a failure for the
	// command too, and the read did not fail (so it is not a network error).
	out.Reset()
	errOut.Reset()
	if code := cli([]string{"-server", srv.URL, "-task", "task-failed", "-progress"}, &out, &errOut); code != 1 {
		t.Fatalf("failed task -progress exit = %d, want 1 (stderr = %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "status error") {
		t.Errorf("stdout = %q", out.String())
	}

	// A refusal is the runtime's own words, and a non-zero exit.
	out.Reset()
	errOut.Reset()
	if code := cli([]string{"-server", srv.URL, "-task", "task-404", "-progress"}, &out, &errOut); code != 1 {
		t.Fatalf("missing task exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "task not found") {
		t.Errorf("stderr = %q", errOut.String())
	}

	want := []string{"POST /api/tasks/task-28/stop", "GET /api/tasks/task-28",
		"GET /api/tasks/task-failed", "GET /api/tasks/task-404"}
	if strings.Join(asked, ", ") != strings.Join(want, ", ") {
		t.Errorf("asked %v, want %v", asked, want)
	}
}

func TestParseDefaultsAreTheDocumentedDemo(t *testing.T) {
	t.Setenv("AUTONOMY_API_URL", "")
	t.Setenv("AUTONOMY_TASK_ID", "")

	o, err := parse(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.server != defaultServer || o.task != demoTaskID || o.description != demoInstruction {
		t.Errorf("defaults = %+v", o)
	}
	if o.context.String() != "project=project-2" || !o.wait || !o.follow {
		t.Errorf("defaults = %+v (context %s)", o, o.context)
	}

	// Naming the task and nothing else continues that task: the instruction is
	// the description its own row carries (docs/http-api.md).
	o, err = parse([]string{"-task", "task-9"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.description != "" || o.task != "task-9" {
		t.Errorf("parse(-task) = %+v", o)
	}

	// Words on the command line are the instruction (flags first: they are flags
	// of this command, and it stops parsing them at the first word), and an empty
	// value takes a context ref back.
	o, err = parse([]string{"-context", "project=", "-wait=false", "open", "the", "front", "entry"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.description != "open the front entry" || len(o.context) != 0 {
		t.Errorf("parse(words) = %+v (context %s)", o, o.context)
	}
	if o.follow {
		t.Error("-wait=false must not try to follow")
	}

	if _, err := parse([]string{"-stop", "-progress"}, io.Discard); err == nil {
		t.Error("-stop and -progress together must be refused")
	}
	if _, err := parse([]string{"-task", "", "-stop"}, io.Discard); err == nil {
		t.Error("-stop without a task must be refused")
	}
}

func TestOneLineIsAPreview(t *testing.T) {
	if got := oneLine("code_edit\n{\"goal\":\"…\"}"); got != "code_edit" {
		t.Errorf("oneLine = %q", got)
	}
	long := strings.Repeat("字", previewRunes+10)
	got := oneLine(long)
	if n := len([]rune(got)); n != previewRunes+1 {
		t.Errorf("oneLine kept %d runes, want %d + the ellipsis", n, previewRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("oneLine = %q", got)
	}
}
