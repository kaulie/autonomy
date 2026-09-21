package autonomy

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A task detail carries the engine's own side of "is it done?": the Completion Contract
// pinned on cycle 1 and the verdicts judged against it. Without it `status=unverified`
// is a word with no evidence behind it — a reader cannot see what was required, who was
// asked, or what came back (docs/verification.md).

// newDetailRuntime is a runtime over a fresh store, the way TaskProgress is served.
func newDetailRuntime(t *testing.T) (*Autonomy, rawStore) {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Autonomy{Store: store}, store
}

func TestTaskProgressCarriesVerification(t *testing.T) {
	rt, store := newDetailRuntime(t)
	const taskID = "task-verify"
	task := &Task{ID: taskID, Description: "land the refactor", Status: TaskStatusUnverified,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}

	// The contract as the first answer wrote it: this side keeps it verbatim.
	const criterion = `{"name":"C2","requirement":"The change is landed on the base branch",` +
		`"evidence":{"source":"step:land.output.merged"},"expect":{"field":"merged","equals":"true"}}`
	if err := store.AppendCompletionContract(ContractCriterion{TaskID: taskID, Idx: 1, PlanID: 3,
		Name: "C2", Criterion: criterion, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	verdict := Verification{TaskID: taskID, PlanID: 4, Cycle: 4, Criterion: "C2",
		Requirement: "The change is landed on the base branch", Method: "registry:git.merge",
		Evidence: `{"slot":"step:land.output.merged","reference":"step:land"}`, Expected: "merged = true",
		Observed: "merged = false", Result: verificationInconclusive,
		Reason: "no step named land ran in this task", CreatedAt: time.Now()}
	if _, err := store.AppendVerification(verdict); err != nil {
		t.Fatal(err)
	}

	progress, err := rt.TaskProgress(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Verification == nil {
		t.Fatal("verification 没带上：unverified 就只剩一个状态字")
	}
	if len(progress.Verification.Contract) != 1 {
		t.Fatalf("contract = %d 条，want 1", len(progress.Verification.Contract))
	}
	got := progress.Verification.Contract[0]
	if got.Idx != 1 || got.Name != "C2" {
		t.Fatalf("contract[0] = %+v，want idx 1 / name C2", got)
	}
	// 判据是**原文**：这一侧不改写它（改写了就不是它被判定时用的那份合同）。
	var parsed map[string]any
	if err := json.Unmarshal(got.Criterion, &parsed); err != nil {
		t.Fatalf("criterion 不是可读 JSON：%v", err)
	}
	if parsed["requirement"] != "The change is landed on the base branch" {
		t.Fatalf("criterion 原文被改写了：%+v", parsed)
	}

	if len(progress.Verification.Verdicts) != 1 {
		t.Fatalf("verdicts = %d 条，want 1", len(progress.Verification.Verdicts))
	}
	v := progress.Verification.Verdicts[0]
	if v.ID == 0 || v.Result != verificationInconclusive || v.Cycle != 4 || v.PlanID != 4 {
		t.Fatalf("verdict = %+v，want 带 id / cycle 4 / plan 4 / inconclusive", v)
	}
	// 三样缺一不可：问的谁（method）、期望什么、实际是什么 —— 只有它们在，判定才是可复核的。
	if v.Method != "registry:git.merge" || v.Expected != "merged = true" || v.Observed != "merged = false" {
		t.Fatalf("verdict 少了可复核的三要素：%+v", v)
	}
	if v.Reason == "" || !strings.Contains(v.Evidence, "step:land.output.merged") {
		t.Fatalf("verdict 少了 reason / evidence：%+v", v)
	}
}

// A task the engine never judged carries **no** verification field: absent says "nothing
// was ever judged", which an empty `{"contract":[],"verdicts":[]}` would blur into
// "the engine looked and found nothing".
func TestTaskProgressOmitsVerificationWhenNothingWasJudged(t *testing.T) {
	rt, store := newDetailRuntime(t)
	const taskID = "task-plain"
	if err := store.UpsertTask(&Task{ID: taskID, Description: "just planning",
		Status: TaskStatusRunning, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	progress, err := rt.TaskProgress(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Verification != nil {
		t.Fatalf("没有判定就不该有 verification：%+v", progress.Verification)
	}
	body, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"verification"`) {
		t.Fatalf("没判过的 task，JSON 里不该出现 verification：%s", body)
	}
}
