package autonomy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stopRecords returns the stop messages an agent's inbox holds, newest last.
func stopRecords(t *testing.T, store Store, agentID int64) []AgentMessage {
	t.Helper()
	msgs, err := store.ListAgentMessages(agentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []AgentMessage
	for _, m := range msgs {
		if m.Kind == MessageKindStop {
			out = append(out, m)
		}
	}
	return out
}

// TestStopByTheUserSaysSo: the door's stop keeps saying what it always said — the record
// names the user, and the task row stays "stopped" (no reason was claimed, so none is
// invented).
func TestStopByTheUserSaysSo(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "user-stop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// markStopped persists through the active store (the runtime's own writer).
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	taskID := "task-user-stop"
	if err := store.UpsertTask(&Task{ID: taskID, Description: "d", Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	inFlightTasks.Store(taskID, cancel)
	t.Cleanup(func() { inFlightTasks.Delete(taskID) })

	rt := &Autonomy{Store: store, AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory())}
	// The stop message is left where the task's agent reads it, so that agent has to
	// exist: an idle task has no inbox to stop into (and nothing to stop).
	agent := rt.AgentFactory.Create(&Task{ID: taskID, Description: "d"})
	stopped, err := rt.StopTask(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != TaskStatusStopped || stopped.Error != "stopped" {
		t.Fatalf("status=%q error=%q, want stopped / stopped", stopped.Status, stopped.Error)
	}
	records := stopRecords(t, store, agent.ID)
	if len(records) != 1 || records[0].Content != "the user stopped the task" {
		t.Fatalf("stop records=%+v, want one saying the user stopped it", records)
	}
}

// TestRestartStopIsRecordedAsTheRuntime: the runtime stopping an in-flight run for a
// restart is recorded as exactly that — the message and the task row name the deploy
// (pipeline id) instead of blaming the user. This is the record that used to lie, and it
// is written on the run's own path (markStopped), so both paths are exercised here.
func TestRestartStopIsRecordedAsTheRuntime(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "restart-stop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	taskID := "task-restart-stop"
	if err := store.UpsertTask(&Task{ID: taskID, Description: "d", Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	inFlightTasks.Store(taskID, context.CancelFunc(cancelRun))
	t.Cleanup(func() { inFlightTasks.Delete(taskID) })

	rt := &Autonomy{Store: store, AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory())}
	agent := rt.AgentFactory.Create(&Task{ID: taskID, Description: "d"})
	// What the platform sends before it restarts us (POST /api/ops/restart-notify).
	rt.restartDrain().begin(RestartNotice{
		RequestID:  "pipeline-test",
		Deployment: "deployment-test",
		Version:    "abc1234",
	}, false)

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShutdown()
	rt.Shutdown(shutdownCtx)

	if runCtx.Err() == nil {
		t.Fatal("shutdown did not cancel the in-flight run")
	}
	got, err := store.GetTask(taskID)
	if err != nil || got == nil {
		t.Fatalf("stored=%+v err=%v", got, err)
	}
	if got.Status != TaskStatusStopped {
		t.Fatalf("status=%q, want stopped", got.Status)
	}
	if !strings.Contains(got.Error, "stopped by the runtime for a restart") ||
		!strings.Contains(got.Error, "requestId=pipeline-test") {
		t.Fatalf("task error=%q, want the runtime's restart reason with the pipeline id", got.Error)
	}
	records := stopRecords(t, store, agent.ID)
	if len(records) == 0 {
		t.Fatal("no stop record")
	}
	content := records[len(records)-1].Content
	if strings.Contains(content, "the user stopped the task") {
		t.Fatalf("stop record=%q still blames the user", content)
	}
	for _, want := range []string{"the runtime stopped the task for a restart", "requestId=pipeline-test", "deployment-test", "version=abc1234"} {
		if !strings.Contains(content, want) {
			t.Fatalf("stop record=%q does not mention %q", content, want)
		}
	}

	// The run loop marks the task stopped too (a cancelled cycle); the reason must still
	// be there when it does — that write is the one production takes.
	if err := rt.runLoop(runCtx, agent, &Task{ID: taskID, Description: "d", Status: TaskStatusRunning}, "d"); err == nil {
		t.Fatal("runLoop returned nil after the stop")
	}
	again, err := store.GetTask(taskID)
	if err != nil || again == nil {
		t.Fatalf("stored=%+v err=%v", again, err)
	}
	if !strings.Contains(again.Error, "stopped by the runtime for a restart") {
		t.Fatalf("after the run loop the task error=%q, want the runtime's restart reason", again.Error)
	}
}

// TestStopReasonDoesNotOutliveItsRun: a reason is cleared where its run leaves in-flight,
// so a later cancellation with no reason keeps writing the untouched "stopped".
func TestStopReasonDoesNotOutliveItsRun(t *testing.T) {
	taskID := "task-stale-reason"
	setStopReason(taskID, StopReason{By: stoppedByRuntime, RequestID: "pipeline-old"})
	t.Cleanup(func() { clearStopReason(taskID) })

	store, err := openStore(filepath.Join(t.TempDir(), "stale.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store
	if err := store.UpsertTask(&Task{ID: taskID, Description: "d", Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}

	clearStopReason(taskID) // the previous run left in-flight
	markStopped(&Task{ID: taskID, Description: "d", Status: TaskStatusRunning})
	got, err := store.GetTask(taskID)
	if err != nil || got == nil {
		t.Fatalf("stored=%+v err=%v", got, err)
	}
	if got.Error != "stopped" {
		t.Fatalf("error=%q, want the plain stopped", got.Error)
	}
}
