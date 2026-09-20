package autonomy

// Building the conversation rows of one run is dialect-neutral: which message a
// run's input becomes, in which order the aggregates and the return follow, and
// what makes a return traceable to its input are properties of the *contract*
// (ConversationStore in src/store.go, docs/llm-message.md) rather than of a
// database. Both engines build the same records and only differ in how they write
// them down, so the record construction lives here — outside every engine file —
// and no engine has to keep a second copy of it in step.

// llmMessageSeqUser is the seq of a run's user-input message. The assistant
// (final return) row is placed one past the aggregated thinking/tool rows, so
// with no aggregated rows it keeps the original seq 1 layout.
const (
	llmMessageSeqUser      = 0
	llmMessageSeqAssistant = 1
)

// inputMessage builds a run's input llm_messages row. Its role names who authored
// the prompt: the user (the default) for the runtime's own prompts, agent when
// another agent delegated this run to the agent that is running it.
func inputMessage(turnID int64, turn ReasonTurn) LLMMessage {
	role := turn.InputRole
	if role == "" {
		role = LLMMessageRoleUser
	}
	return LLMMessage{
		TurnID:      turnID,
		TaskID:      turn.TaskID,
		AgentID:     turn.AgentID,
		Cycle:       turn.Cycle,
		Seq:         llmMessageSeqUser,
		Role:        role,
		Content:     turn.Input,
		LLMProvider: turn.LLMProvider,
		Model:       turn.Model,
		RunID:       turn.RunID,
		Status:      turn.Status,
		CreatedAt:   turn.CreatedAt,
	}
}
