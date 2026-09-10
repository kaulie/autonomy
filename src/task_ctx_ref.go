package autonomy

// TaskCtxRef is a reference to a context entity for a task
// a task can have multiple context references
type TaskCtxRef struct {
	TaskID          string
	ContextEntityID string
}

type TaskCtxManager struct {
	Task2CtxRefs map[string][]TaskCtxRef
}

func NewTaskCtxManager() *TaskCtxManager {
	return &TaskCtxManager{
		Task2CtxRefs: make(map[string][]TaskCtxRef),
	}
}

func (t *TaskCtxManager) AddTaskCtxRef(taskID string, contextEntityID string) {
	ctxRefs := t.Task2CtxRefs[taskID]
	if ctxRefs == nil {
		ctxRefs = []TaskCtxRef{}
	}
	ctxRefs = append(ctxRefs, TaskCtxRef{
		TaskID:          taskID,
		ContextEntityID: contextEntityID,
	})
	t.Task2CtxRefs[taskID] = ctxRefs
}

func (t *TaskCtxManager) GetTaskCtxRefs(taskID string) []TaskCtxRef {
	return t.Task2CtxRefs[taskID]
}
