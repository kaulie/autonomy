package autonomy

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is the service side of a graceful restart.
//
// The deployment platform (agent-control-plane-deployment) restarts a service by
// stopping and starting it. A service that has registered two endpoints is restarted
// **gracefully** instead: the platform first POSTs a notice to the *notify* URL, then
// polls the *status* URL every 15s until the answer says a restart is safe — or until
// its own maximum wait runs out, in which case it restarts anyway.
//
// Autonomy's side of that contract is two endpoints (src/http_server.go):
//
//	POST /api/ops/restart-notify   a restart is coming: stop starting new runs
//	GET  /api/ops/restart-status   is it safe to restart? (canRestart / canDeploy / ready)
//
// What "safe" means here is: nothing is being run. A restart cuts whatever is in
// flight, and a run is not something to cut — it is a decision cycle that may already
// have changed the world (a branch pushed, a deploy triggered) — so the drain is:
//
//  1. stop starting new runs. An instruction that arrives while draining is still
//     accepted (it is a row in its agent's inbox, src/inbox.go), but its agent does not
//     start it; the process that comes up after the restart does
//     (resumeAcceptedInstructions). Nothing a caller handed over is lost.
//  2. let the runs already in flight finish — they are what the poll counts
//     (runningTaskIDs: the same registry a stop cancels from).
//  3. answer canRestart the moment that count reaches zero.
//
// The process's own end tells the same story (cmd/autonomyd): SIGTERM — how
// scripts/stop.sh stops this service — is handled as a drain too (Autonomy.Shutdown),
// so the store and the provider sessions are closed by Autonomy.Close instead of being
// killed mid-write.
//
// A drain is bounded: if no restart follows within AUTONOMY_DRAIN_TIMEOUT (10 minutes
// by default, the platform's own default maximum wait), autonomy resumes by itself. A
// deploy that dies after the notice (a packaging failure, a conflict) must not leave
// the service holding every new instruction forever.
//
// Nothing here is a promise that a restart happened: the platform's stop/start is what
// does that. A service whose process is replaced comes back with an empty drain (the
// state is memory) and with the instructions the drain held, in its agents' inboxes.

const (
	// defaultDrainTimeout is how long a drain may last before the runtime resumes on
	// its own (see the file comment). It mirrors the deployment platform's own default
	// maximum graceful wait, GRACEFUL_RESTART_MAX_WAIT_MS (10 minutes).
	defaultDrainTimeout = 10 * time.Minute
	// drainTimeoutEnv overrides it with a Go duration ("2m", "30s"). 0 (or less) means
	// the drain never expires on its own: it waits for the platform.
	drainTimeoutEnv = "AUTONOMY_DRAIN_TIMEOUT"
	// shutdownGraceEnv is how long a SIGTERM'd process gives the runs in flight to
	// finish before it stops them itself (Autonomy.Shutdown). It must stay inside
	// scripts/stop.sh's own TERM → KILL window (15s).
	shutdownGraceEnv     = "AUTONOMY_SHUTDOWN_GRACE"
	defaultShutdownGrace = 10 * time.Second
)

// RestartNotice is what the deployment platform announces a restart with
// (POST /api/ops/restart-notify). RequestID (the platform's deploy request id) is the
// only field this runtime requires; the rest is what the platform says about itself
// and about the restart, kept so the status endpoint can answer "who is restarting us,
// and with what?" instead of only "yes/no".
type RestartNotice struct {
	ServiceID  string `json:"serviceId,omitempty"`
	RequestID  string `json:"requestId"`
	Deployment string `json:"deployment,omitempty"`
	Version    string `json:"version,omitempty"`
	Message    string `json:"message,omitempty"`
}

// RestartStatus is what GET /api/ops/restart-status answers.
//
// canRestart is the field the deployment platform reads; `canDeploy` and `ready` are
// the aliases its other convention uses, and all three say the same thing (any of them
// true lets the deploy proceed), so a caller that learned one name does not have to
// learn this service's.
//
// running is what a restart would cut right now; running_tasks names them. held is the
// instructions the drain is holding back: they are not in flight (they are rows), and
// the next process runs them.
type RestartStatus struct {
	CanRestart bool `json:"canRestart"`
	CanDeploy  bool `json:"canDeploy"`
	Ready      bool `json:"ready"`
	// Draining is whether a restart has been announced and not yet carried out. It is
	// the difference between "nothing is running now" and "nothing is running because we
	// stopped starting things".
	Draining bool `json:"draining"`
	// Running counts the task runs in flight right now, and RunningTasks names them.
	Running      int      `json:"running"`
	RunningTasks []string `json:"runningTasks,omitempty"`
	// Held counts the agents this process holds an instruction back for: accepted during
	// the drain, not started. It is memory, so it is 0 again after the restart — the
	// instructions themselves are rows (agent_messages).
	Held int `json:"held,omitempty"`
	// Reason is the answer in words, for a human reading a deploy log.
	Reason string `json:"reason"`
	// Notice is what the platform announced and NotifiedAt when it did; Deadline is when
	// this drain gives up waiting and resumes by itself (absent when it never does).
	Notice     *RestartNotice `json:"notice,omitempty"`
	NotifiedAt *time.Time     `json:"notifiedAt,omitempty"`
	Deadline   *time.Time     `json:"deadline,omitempty"`
}

// RestartDrain is one runtime's drain: whether a restart has been announced, what was
// announced, and when it stops waiting for the platform on its own.
type RestartDrain struct {
	mu      sync.Mutex
	active  bool
	notice  RestartNotice
	beganAt time.Time
	timeout time.Duration
	// resume is how the drain hands the held work back: the runtime starts the
	// instructions it held when the drain ends without a restart (the safety timeout).
	resume func(reason string)
	timer  *time.Timer
}

// newRestartDrain builds the drain of one runtime. A nil drain is inert: every method
// tolerates one, so a runtime assembled by hand (a test, an embedder) behaves as if
// nothing was ever announced.
func newRestartDrain(resume func(reason string)) *RestartDrain {
	return &RestartDrain{timeout: drainTimeout(), resume: resume}
}

// drainTimeout reads how long a drain may wait for the platform before resuming by
// itself (AUTONOMY_DRAIN_TIMEOUT; empty = the default, 0 or less = never).
func drainTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(drainTimeoutEnv))
	if raw == "" {
		return defaultDrainTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] %s=%q 不是合法的 duration（如 2m），按默认 %s 处理\n",
			drainTimeoutEnv, raw, defaultDrainTimeout)
		return defaultDrainTimeout
	}
	return d
}

// begin enters the drain for one announced restart. waitForPlatform arms the safety
// timer — a notify is an announcement, a shutdown is not (nobody is going to restart
// us, we are the ones leaving). A second notify while draining refreshes the notice,
// which is what a platform that retried its deploy looks like.
func (d *RestartDrain) begin(n RestartNotice, waitForPlatform bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.active = true
	d.notice = n
	d.beganAt = time.Now()
	timeout := d.timeout
	if waitForPlatform && timeout > 0 {
		d.timer = time.AfterFunc(timeout, d.expire)
	}
	d.mu.Unlock()
	if waitForPlatform {
		fmt.Fprintf(os.Stderr, "[autonomy] graceful: drain 开始 requestId=%s deployment=%s version=%s（最长 %s）\n",
			noticeField(n.RequestID), noticeField(n.Deployment), noticeField(n.Version), timeout)
	}
}

// noticeField is one field of an announced notice, with a placeholder for an empty one
// so a log line never reads as if the platform had said nothing at all.
func noticeField(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return strings.TrimSpace(v)
}

// expire is the safety timer: nobody restarted this process, so the drain ends and the
// instructions it held start running, rather than being held forever.
func (d *RestartDrain) expire() {
	d.mu.Lock()
	timeout := d.timeout
	d.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[autonomy] graceful: %s 内没有等到重启，drain 自动结束，恢复新 run\n", timeout)
	d.finish("drain timeout")
}

// finish leaves the drain and asks the runtime to start what the drain held.
func (d *RestartDrain) finish(reason string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.active {
		d.mu.Unlock()
		return
	}
	d.active = false
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	resume := d.resume
	d.mu.Unlock()
	if resume != nil {
		resume(reason)
	}
}

// stop deactivates the drain and its timer without handing anything back: teardown,
// where there is nothing left to resume.
func (d *RestartDrain) stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active = false
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// paused reports whether new runs are held back right now. It is what the inbox asks
// before it starts an agent (src/inbox.go).
func (d *RestartDrain) paused() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active
}

// fill writes the drain's own part of one status answer: whether we are draining, what
// was announced, and when the drain expires.
func (d *RestartDrain) fill(st *RestartStatus) {
	if d == nil || st == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st.Draining = d.active
	if !d.active {
		return
	}
	notice := d.notice
	began := d.beganAt
	st.Notice = &notice
	st.NotifiedAt = &began
	if d.timeout > 0 {
		deadline := began.Add(d.timeout)
		st.Deadline = &deadline
	}
}

// RestartNotify announces a restart: the runtime stops starting new runs, and the
// answer reports the restart that would cut what is in flight
// (POST /api/ops/restart-notify).
func (r *Autonomy) RestartNotify(n RestartNotice) RestartStatus {
	if r == nil {
		return RestartStatus{Reason: "no runtime"}
	}
	r.restartDrain().begin(n, true)
	return r.RestartStatus()
}

// RestartStatus answers the deployment platform's poll: is a restart safe now, and why
// (GET /api/ops/restart-status).
func (r *Autonomy) RestartStatus() RestartStatus {
	tasks := runningTaskIDs()
	st := RestartStatus{
		Running:      len(tasks),
		RunningTasks: tasks,
	}
	if r != nil {
		st.Held = r.agentInbox().heldAgents()
		r.restartDrain().fill(&st)
	}
	canRestart := st.Running == 0
	st.CanRestart, st.CanDeploy, st.Ready = canRestart, canRestart, canRestart
	st.Reason = restartReason(st)
	return st
}

// restartReason is the status answer in words: what is in the way of a restart, and —
// when the drain is holding instructions back — where they are going to run.
func restartReason(st RestartStatus) string {
	if st.Running > 0 {
		if st.Draining {
			return fmt.Sprintf("%d run(s) in flight; new instructions are held until the restart", st.Running)
		}
		return fmt.Sprintf("%d run(s) in flight", st.Running)
	}
	if st.Draining {
		if st.Held > 0 {
			return fmt.Sprintf("no run in flight; %d held instruction(s) start in the next process", st.Held)
		}
		return "no run in flight; safe to restart"
	}
	return "no run in flight; restart not announced"
}

// restartDrain is this runtime's drain, built on demand the way the inbox is, so a
// runtime assembled by hand has one too.
func (r *Autonomy) restartDrain() *RestartDrain {
	if r == nil {
		return nil
	}
	if r.Drain == nil {
		r.Drain = newRestartDrain(func(reason string) { r.resumeHeldInstructions(reason) })
	}
	return r.Drain
}

// resumeHeldInstructions starts the agents this process held back while draining: the
// drain ended without a restart (the safety timeout), so the instructions that arrived
// in the meantime are the agents' to process again (the inbox's own resumePaused).
func (r *Autonomy) resumeHeldInstructions(reason string) {
	if r == nil {
		return
	}
	if n := r.agentInbox().resumePaused(); n > 0 {
		fmt.Fprintf(os.Stderr, "[autonomy] graceful: drain 结束（%s），%d 只 agent 继续处理排队的指令\n", reason, n)
	}
}

// resumeAcceptedInstructions starts the instructions a *previous* process left queued:
// the ones a restart in the middle of a drain held back. They are rows in their agents'
// inboxes (agent_messages, status queued — never started), so nothing about them is
// guessed: an open task (pending / running) whose agent has a queued message and no
// message the previous process died *on* gets its consumer started, and the consumer
// claims exactly what is there.
//
// A message left `running` is deliberately not started here. That is a run a process
// died in the middle of: the queue puts it back in front of the agent the next time the
// agent is started (InboxStore.RequeueRunningMessages, src/inbox.go), and doing that
// here would re-run a cycle that may already have changed the world, on boot, without
// anyone having asked. It is not lost either — the next instruction for that task
// starts it, as it always did.
func (r *Autonomy) resumeAcceptedInstructions() {
	if r == nil {
		return
	}
	store := r.Store
	if store == nil {
		store = activeStore()
	}
	if store == nil {
		return
	}
	tasks, err := store.ListTasks()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] resume queued instructions: %v\n", err)
		return
	}
	for _, task := range tasks {
		if task == nil || task.AgentID == 0 {
			continue
		}
		if task.Status != TaskStatusPending && task.Status != TaskStatusRunning {
			continue
		}
		queued, err := store.CountQueuedMessages(task.AgentID)
		if err != nil || queued == 0 {
			continue
		}
		messages, err := store.ListAgentMessages(task.AgentID, 0)
		if err != nil {
			continue
		}
		interrupted := false
		for _, msg := range messages {
			if msg.Status == MessageStatusRunning {
				interrupted = true
				break
			}
		}
		if interrupted {
			continue
		}
		agent, err := r.resumeAgentForTask(task)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] resume task %s: %v\n", task.ID, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "[autonomy] %s 有一条排队中的指令（task %s），本次启动继续处理\n", agent.Name, task.ID)
		r.agentInbox().start(agent)
	}
}

// Shutdown is the runtime's own side of a graceful process stop — SIGTERM, which is how
// scripts/stop.sh and the deployment platform end this process:
//
//   - no new run starts (a drain that never expires: nothing is going to restart us, we
//     are the ones leaving),
//   - the runs in flight are given until ctx is done to come back on their own — the
//     platform has already waited for them (canRestart), so this is the forced case's
//     grace,
//   - whatever is still running is then stopped the way POST /api/tasks/{id}/stop stops
//     it, and given a moment to unwind, so its message and its task end as `stopped`
//     instead of as a run nobody will ever finish.
//
// The caller closes the runtime afterwards (Autonomy.Close), which is where the provider
// sessions, the bridges and the store are torn down.
func (r *Autonomy) Shutdown(ctx context.Context) {
	if r == nil {
		return
	}
	r.restartDrain().begin(RestartNotice{RequestID: "shutdown", Message: "autonomy is shutting down"}, false)
	if waitForIdleRuns(ctx) {
		fmt.Fprintf(os.Stderr, "[autonomy] graceful: 进程退出，没有在途 run\n")
		return
	}
	stopped := 0
	for _, taskID := range runningTaskIDs() {
		if _, err := r.StopTask(taskID); err == nil {
			stopped++
		}
	}
	if stopped == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "[autonomy] graceful: 进程退出，停掉 %d 个在途 run\n", stopped)
	waitForIdleRuns(ctx)
}

// waitForIdleRuns waits until nothing is in flight, or ctx is done (true when nothing
// is running).
func waitForIdleRuns(ctx context.Context) bool {
	if len(runningTaskIDs()) == 0 {
		return true
	}
	if ctx == nil {
		return false
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return len(runningTaskIDs()) == 0
		case <-ticker.C:
			if len(runningTaskIDs()) == 0 {
				return true
			}
		}
	}
}

// ShutdownGrace is how long a stopping process gives the runs in flight to come back
// before it stops them itself (AUTONOMY_SHUTDOWN_GRACE; empty = the default, negative =
// no wait at all).
func ShutdownGrace() time.Duration {
	raw := strings.TrimSpace(os.Getenv(shutdownGraceEnv))
	if raw == "" {
		return defaultShutdownGrace
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] %s=%q 不是合法的 duration（如 10s），按默认 %s 处理\n",
			shutdownGraceEnv, raw, defaultShutdownGrace)
		return defaultShutdownGrace
	}
	if d < 0 {
		return 0
	}
	return d
}

// runningTaskIDs is every task with a run in flight right now — what a restart would
// cut. It reads the same registry POST /api/tasks/{id}/stop cancels from, so "in flight"
// means the same thing to a stop, to a restart and to a shutdown.
func runningTaskIDs() []string {
	var ids []string
	inFlightTasks.Range(func(key, _ any) bool {
		if id, ok := key.(string); ok && strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
		return true
	})
	sort.Strings(ids)
	return ids
}
