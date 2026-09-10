package autonomy

import "strings"

// completionPrinciplesByGoalType maps a GoalType to the completion principles
// rendered into the {{COMPLETION_PRINCIPLES}} placeholder of AGENT_V2.md.
var completionPrinciplesByGoalType = map[GoalType]string{
	GoalType_FEATURE:                           "- Deliver a working implementation that satisfies the requested feature.\n- Keep changes scoped to the feature; do not expand scope.\n- Verify behavior with tests or a runnable demonstration before reporting done.",
	GoalType_Resolve_ISSUE:                     "- Fix the reported issue at its root cause.\n- Reproduce the issue before and after the fix.\n- Add a regression test where practical.",
	GoalType_Optimize_PERFORMANCE:              "- Identify a measurable performance baseline.\n- Optimize only demonstrated bottlenecks.\n- Report measured improvement, not assumed improvement.",
	GoalType_Serivce_maintenance:               "- Keep the service available and healthy.\n- Prefer safe, reversible operations.\n- Verify service health after changes.",
	GoalType_Improve_DOCUMENTATION_READABILITY: "- Improve clarity and accuracy of documentation.\n- Preserve technical correctness; do not invent facts.\n- Keep docs consistent with actual behavior.",
	GoalType_Improve_TEST_COVERAGE:             "- Add tests for meaningful behavior and edge cases.\n- Prefer deterministic, maintainable tests.\n- Do not weaken existing tests.",
	GoalType_Improve_CODE_QUALITY:              "- Improve code clarity, structure, and maintainability.\n- Preserve existing behavior unless explicitly required.\n- Keep changes focused and reviewable.",
	GoalType_Improve_CODE_READABILITY:          "- Make code easier to read and reason about.\n- Avoid changing behavior while refactoring.\n- Follow existing conventions.",
	GoalType_Improve_CODE_MAINTAINABILITY:      "- Reduce coupling and hidden dependencies.\n- Keep interfaces small and explicit.\n- Preserve behavior and tests.",
	GoalType_Improve_CODE_SECURITY:             "- Remove the vulnerability or unsafe pattern.\n- Do not introduce new attack surface.\n- Verify the fix with a targeted check.",
	GoalType_Improve_CODE_PERFORMANCE:          "- Optimize only measured hot paths.\n- Preserve correctness and behavior.\n- Report before/after evidence.",
	GoalType_Improve_CODE_RELIABILITY:          "- Make behavior more robust under failure.\n- Handle errors explicitly; do not mask them.\n- Add tests for the failure scenarios addressed.",
}

const completionPrinciplesDefault = "- Complete only the requested goal.\n- Do not invent facts or side effects.\n- Report done only when the goal is verified by evidence."

// CompletionPrinciplesFor returns the completion principles text for a goal
// type, falling back to a generic set for unknown/empty types.
func CompletionPrinciplesFor(gt GoalType) string {
	if s := strings.TrimSpace(string(gt)); s != "" {
		if principles, ok := completionPrinciplesByGoalType[GoalType(s)]; ok {
			return principles
		}
	}
	return completionPrinciplesDefault
}
