package autonomy

import (
	"fmt"
	"strings"
)

// One agent's message log, as the UI renders it: what was addressed to it and what it answered.
//
// The two halves come from the two places an agent's messages actually live. **Received** is the
// inbox (src/inbox.go): an instruction from the user, a delegated prompt from another agent, a
// stop from the runtime — the agent's own queue, in arrival order. **Sent** is the agent's turns
// (the reason-turn log): each turn carries what it was asked (its input, the prompt the runtime
// rendered) and what it answered (its output), which is what "the agent replied" means here.
type AgentMessages struct {
	AgentID int64  `json:"agent_id"`
	Agent   string `json:"agent,omitempty"`
	// Received is the inbox in arrival order (oldest first): the order the agent processed it.
	Received []AgentMessage `json:"received"`
	// Sent is the agent's turns, newest first, each with the input it answered and its output.
	Sent []AgentTurnMessage `json:"sent"`
}

// AgentTurnMessage is one of an agent's answers, in the shape a message view wants: one line per
// exchange rather than the whole record a turn page renders.
type AgentTurnMessage struct {
	TurnID     int64  `json:"turn_id"`
	TaskID     string `json:"task_id,omitempty"`
	Cycle      int    `json:"cycle,omitempty"`
	Status     string `json:"status,omitempty"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// AgentMessages reads one agent's message log. limit <= 0 means "everything", and only one of the
// two halves is capped by it (the inbox's oldest rows are its queue; a UI asks for a page of each).
func (r *Autonomy) AgentMessages(agentID int64, limit int) (*AgentMessages, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	if agentID == 0 {
		return nil, fmt.Errorf("empty agent id")
	}
	agent, err := r.agentStore().GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, errAgentNotFound
	}
	out := &AgentMessages{AgentID: agentID, Agent: agent.Name, Received: []AgentMessage{}, Sent: []AgentTurnMessage{}}
	if received, err := r.Store.ListAgentMessages(agentID, limit); err == nil {
		out.Received = received
	}
	// Turns are filtered by agent *name* (TurnQuery.Agent matches agents.name, which is what a
	// facet offers a human) — the row's name, not the id this call was given.
	store, err := r.turnQueryStore()
	if err != nil {
		return out, nil
	}
	turns, _, err := store.QueryTurns(TurnQuery{Agent: strings.TrimSpace(agent.Name), Limit: limit})
	if err != nil {
		return out, nil
	}
	for _, turn := range turns {
		out.Sent = append(out.Sent, AgentTurnMessage{
			TurnID:     turn.ID,
			TaskID:     turn.TaskID,
			Cycle:      turn.Cycle,
			Status:     turn.Status,
			Input:      turn.Input,
			Output:     firstNonEmptyText(turn.NormalizedOutput, turn.Output),
			DurationMS: turn.DurationMS,
			CreatedAt:  turn.CreatedAt,
		})
	}
	return out, nil
}

// firstNonEmptyText is the normalized output when the runtime derived one, else the raw answer:
// what a message view shows on its line.
func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
