// Command autonomy sends one instruction to a running Autonomy runtime and, by
// default, follows it to its end.
//
// It is a client of the runtime's HTTP API and nothing more (docs/http-api.md):
// no part of the runtime is linked into this binary, so it can be run from
// anywhere — against the service the deployment platform started (bin/autonomyd,
// default 127.0.0.1:4300, the port its service contract declares) or against a
// local one. The calls are the documented
// ones, so curl says the same thing:
//
//	POST /api/tasks                             one instruction (what this sends)
//	GET  /api/tasks/{id}                        its progress (-wait, -progress)
//	GET  /api/tasks/{id}/agents/{id}/events     the conversation (-follow)
//	POST /api/tasks/{id}/stop                   stop what the agent is on (-stop)
//
// The runtime's own words are what come back: a refusal is the API's {"error"}
// and a task's outcome is the status on its row (completed, blocked,
// need_input, unverified, error, stopped) — a run that failed or never held up
// is a non-zero exit, everything else is 0. -progress exits by the status it
// read, so `autonomy -task x -progress` is also a way to ask "is it done, and
// did it end well?".
//
//	autonomy -description "开放服务契约的前端入口"
//	autonomy -task task-28 -description "接着上次那条，把 PR 提了"   # continue that task
//	autonomy -task task-28                                          # instruction is the row's description
//	autonomy -task task-28 -progress                                # just look
//	autonomy -task task-28 -stop                                    # stop what it is on
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// defaultServer is where the deployment's runtime listens: the port the
	// service contract declares for autonomy (`4300`). The platform injects it as
	// SERVICE_PORT and scripts/start.sh falls back to the same number, so a
	// runtime started by hand with AUTONOMY_HTTP_ADDR wants -server (or
	// AUTONOMY_API_URL) to say where it is.
	defaultServer = "http://127.0.0.1:4300"
	// demoInstruction and demoTaskID are what this command sends when it is
	// given nothing at all — the demo README documents (`go run ./cmd/autonomy`)
	// and the world the runtime seeds for it (cmd/autonomyd/world.go).
	demoInstruction = "开放服务契约的前端入口，提交commit，提PR,merge代码后部署上线"
	demoTaskID      = "task-28"
	// An instruction is one run, and a run is not cut by a wall clock until this
	// long has passed — the same patience the in-process door had.
	defaultTimeout = 30 * time.Minute
	defaultPoll    = 2 * time.Second
	// What this prints of a conversation item is a preview; the record is the
	// runtime's llm_messages.
	previewRunes = 240
)

type options struct {
	server      string
	task        string
	description string
	domain      string
	goal        string
	context     contextRefs
	wait        bool
	follow      bool
	timeout     time.Duration
	poll        time.Duration
	stop        bool
	progress    bool
	json        bool
}

// contextRefs is -context key=value, repeatable. An empty value drops the key,
// so the default ref can be taken back with -context project=.
type contextRefs map[string]string

func (r contextRefs) String() string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+r[k])
	}
	return strings.Join(parts, ",")
}

func (r contextRefs) Set(v string) error {
	key, value, ok := strings.Cut(v, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return fmt.Errorf("want key=value, got %q", v)
	}
	if value = strings.TrimSpace(value); value == "" {
		delete(r, key)
		return nil
	}
	r[key] = value
	return nil
}

const usageHeader = `autonomy sends one instruction to a running Autonomy runtime over HTTP (docs/http-api.md)
and, by default, follows the run it becomes.

usage: autonomy [flags] [more words of the instruction]

`

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parse(args []string, stderr io.Writer) (options, error) {
	o := options{
		server:  envOr("AUTONOMY_API_URL", defaultServer),
		task:    envOr("AUTONOMY_TASK_ID", demoTaskID),
		context: contextRefs{"project": "project-2"},
		wait:    true,
		follow:  true,
		timeout: defaultTimeout,
		poll:    defaultPoll,
	}
	fs := flag.NewFlagSet("autonomy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usageHeader)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.server, "server", o.server, "runtime base URL (env AUTONOMY_API_URL)")
	fs.StringVar(&o.task, "task", o.task, "task the instruction is for; empty asks the runtime for a new one (env AUTONOMY_TASK_ID)")
	fs.StringVar(&o.description, "description", "", "the instruction (positional words are appended); empty continues the task's own description")
	fs.StringVar(&o.domain, "domain", "", "task domain (default: the runtime's)")
	fs.StringVar(&o.goal, "goal", "", "goal type, e.g. dev_feature (default: the runtime's)")
	fs.Var(o.context, "context", "context_ref key=value, repeatable; an empty value drops the key")
	fs.BoolVar(&o.wait, "wait", o.wait, "follow the task until its status is no longer running/pending")
	fs.BoolVar(&o.follow, "follow", o.follow, "print the run's conversation while waiting")
	fs.DurationVar(&o.poll, "poll", o.poll, "how often the task's status is read while waiting")
	fs.DurationVar(&o.timeout, "timeout", o.timeout, "give up waiting after this long; the run goes on")
	fs.BoolVar(&o.stop, "stop", false, "stop the message the task's agent is on, instead of sending one")
	fs.BoolVar(&o.progress, "progress", false, "print the task's progress and exit, instead of sending one (exit code = that status)")
	fs.BoolVar(&o.json, "json", false, "print the API's responses as JSON")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if words := fs.Args(); len(words) > 0 {
		o.description = strings.TrimSpace(o.description + " " + strings.Join(words, " "))
	}
	o.description = strings.TrimSpace(o.description)
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if o.description == "" && !o.stop && !o.progress && !set["task"] {
		// Nothing was asked for and no task was named: this is the demo.
		o.description = demoInstruction
	}
	if o.stop && o.progress {
		return o, errors.New("-stop and -progress are two different things: pick one")
	}
	if (o.stop || o.progress) && strings.TrimSpace(o.task) == "" {
		return o, errors.New("-task is required with -stop and -progress")
	}
	if !o.wait {
		// There is nothing to follow while not waiting.
		o.follow = false
	}
	return o, nil
}

func main() {
	os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr))
}

// cli is main's whole body, so what it prints and what it exits with can be
// tested without a process.
func cli(args []string, stdout, stderr io.Writer) int {
	o, err := parse(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, err := newClient(o.server)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	// Ctrl-C ends the client, not the run: the run is the runtime's, and -stop
	// is how to end it.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch {
	case o.progress:
		return showProgress(ctx, c, o, stdout, stderr)
	case o.stop:
		return stopRun(ctx, c, o, stdout, stderr)
	default:
		return submit(ctx, c, o, stdout, stderr)
	}
}

// submit is the instruction itself: POST /api/tasks. It answers as soon as the
// message is in the agent's queue — the run happens on the runtime — so -wait is
// what turns the receipt into "and this is how it ended".
func submit(ctx context.Context, c *client, o options, stdout, stderr io.Writer) int {
	req := acceptRequest{
		TaskID:      o.task,
		Description: o.description,
		Domain:      o.domain,
		GoalType:    o.goal,
		ContextRef:  o.context,
	}
	var acc acceptResponse
	if err := c.post(ctx, "/api/tasks", req, &acc); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if o.json {
		writeJSON(stdout, acc)
	} else {
		fmt.Fprintf(stdout, "task %s agent %d status %s message %d queued %d\n",
			acc.TaskID, acc.AgentID, acc.Status, acc.MessageID, acc.Queued)
	}
	if !o.wait {
		return 0
	}
	return wait(ctx, c, o, acc, stdout, stderr)
}

// wait follows the task the instruction was accepted for until its status says
// the agent is no longer on it (GET /api/tasks/{id}). The status is the task's,
// so what is followed is its runs — the one this instruction became, and any
// message it was queued behind.
func wait(ctx context.Context, c *client, o options, acc acceptResponse, stdout, stderr io.Writer) int {
	cursor := int64(0)
	if o.follow && !o.json {
		// Prime the conversation cursor: following then prints what happens from
		// this instruction on, not the conversation the agent already had.
		var first streamResponse
		if err := c.get(ctx, eventsPath(acc.TaskID, acc.AgentID, 0), &first); err == nil {
			cursor = first.NextPollAfterSeq
		}
	}
	deadline := time.Now().Add(o.timeout)
	last := ""
	for {
		if o.follow && !o.json {
			cursor = follow(ctx, c, acc, cursor, stdout)
		}
		var progress taskProgress
		if err := c.get(ctx, progressPath(acc.TaskID), &progress); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if progress.Status != last {
			last = progress.Status
			if !o.json {
				fmt.Fprintf(stdout, "[task %s] status %s%s\n", progress.TaskID, progress.Status, planNote(progress))
			}
		}
		if over(progress.Status) {
			switch {
			case o.json:
				writeJSON(stdout, progress)
			case progress.Error != "":
				fmt.Fprintf(stdout, "[task %s] status %s: %s\n", progress.TaskID, progress.Status, oneLine(progress.Error))
			}
			return exitFor(progress.Status)
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stderr, "still %s after %s; the run goes on — look with -progress, end it with -stop\n",
				progress.Status, o.timeout)
			return 1
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(stderr, "interrupted; task %s is still running (end it with -stop)\n", progress.TaskID)
			return 130
		case <-time.After(o.poll):
		}
	}
}

// over is whether a status is an outcome rather than work in progress. The
// runtime writes running / pending while a message is being processed and the
// outcome when it is over (src/task.go), so anything else ends the wait.
func over(status string) bool {
	return status != "" && status != "running" && status != "pending"
}

// exitFor is what the command exits with for a task's outcome: a run that failed
// (error) or never held up (unverified) is non-zero; one that is finished
// (completed), is waiting on something (blocked / need_input) or was stopped is not.
func exitFor(status string) int {
	switch status {
	case "error", "unverified":
		return 1
	}
	return 0
}

// follow prints the run's conversation as it arrives: GET .../events carries the
// conversation's own cursor (llm_messages.id), so each poll returns what was
// written since the last one. A read that fails is not fatal — the run is the
// runtime's, and the status poll is what says how it ends.
func follow(ctx context.Context, c *client, acc acceptResponse, cursor int64, stdout io.Writer) int64 {
	var stream streamResponse
	if err := c.get(ctx, eventsPath(acc.TaskID, acc.AgentID, cursor), &stream); err != nil {
		return cursor
	}
	for _, ev := range stream.Events {
		fmt.Fprintf(stdout, "[llm %d] %s %s\n", ev.MessageSeq, ev.Role, oneLine(ev.Content))
	}
	return stream.NextPollAfterSeq
}

// planNote is where the last decision got to, on the status line.
func planNote(p taskProgress) string {
	if len(p.Plans) == 0 {
		return ""
	}
	last := p.Plans[len(p.Plans)-1]
	parts := []string{fmt.Sprintf("cycle=%d", last.Cycle)}
	if last.DecisionType != "" {
		parts = append(parts, "decision="+last.DecisionType)
	}
	if last.StepCount > 0 {
		parts = append(parts, fmt.Sprintf("steps=%d/%d", last.Executed, last.StepCount))
	}
	if last.Outcome != "" {
		parts = append(parts, "outcome="+last.Outcome)
	}
	if last.Need != "" {
		parts = append(parts, "need="+oneLine(last.Need))
	}
	return " " + strings.Join(parts, " ")
}

// showProgress is GET /api/tasks/{id} on its own: what the task is, where it
// stands, and the plans it has run with the steps they planned. Its exit code is
// the status it read, the same way a wait's is the outcome it saw.
func showProgress(ctx context.Context, c *client, o options, stdout, stderr io.Writer) int {
	var progress taskProgress
	if err := c.get(ctx, progressPath(o.task), &progress); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if o.json {
		writeJSON(stdout, progress)
		return exitFor(progress.Status)
	}
	printProgress(stdout, progress)
	return exitFor(progress.Status)
}

// stopRun is POST /api/tasks/{id}/stop: the message the agent is on is
// cancelled, and what was accepted before it stays in the queue for it next.
func stopRun(ctx context.Context, c *client, o options, stdout, stderr io.Writer) int {
	var resp stopResponse
	if err := c.post(ctx, stopPath(o.task), nil, &resp); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if o.json {
		writeJSON(stdout, resp)
	} else {
		fmt.Fprintf(stdout, "task %s %s\n", resp.TaskID, resp.Status)
	}
	return 0
}

func printProgress(w io.Writer, p taskProgress) {
	fmt.Fprintf(w, "task %s  status %s  agent %d  domain %s\n", p.TaskID, p.Status, p.AgentID, p.Domain)
	if p.Description != "" {
		fmt.Fprintf(w, "  %s\n", oneLine(p.Description))
	}
	if p.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", oneLine(p.Error))
	}
	for _, plan := range p.Plans {
		fmt.Fprintf(w, "  plan %d cycle %d %s", plan.PlanID, plan.Cycle, plan.DecisionType)
		if plan.StepCount > 0 {
			fmt.Fprintf(w, " steps %d/%d", plan.Executed, plan.StepCount)
		}
		if plan.Outcome != "" {
			fmt.Fprintf(w, " outcome %s", plan.Outcome)
		}
		fmt.Fprintln(w)
		for _, step := range plan.Steps {
			name := step.Capability
			if name == "" {
				name = step.Name
			}
			fmt.Fprintf(w, "    step %d %s %s", step.Idx, name, step.Status)
			if step.Error != "" {
				fmt.Fprintf(w, ": %s", oneLine(step.Error))
			}
			fmt.Fprintln(w)
		}
	}
}

// writeJSON is an API response as it came, for -json.
func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// oneLine is a value for a terminal line: its first line, trimmed and capped.
// It is a preview — the record is what the runtime persisted.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > previewRunes {
		s = strings.TrimRight(string(r[:previewRunes]), " ") + "…"
	}
	return s
}
