package autonomy

import "time"

// Goal is a user-defined goal.
type Goal struct {
	ID          string
	Description string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CanonicalGoal is a normalized goal. which can be globally shared in whole agent lifecycle.
type CanonicalGoal struct {
	ID          string
	Description string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	GoalType    GoalType
}

type GoalType string

const (
	//user-facing goals
	GoalType_FEATURE       = "dev_feature"
	GoalType_Resolve_ISSUE = "resolve_issue"

	//internal goals

	//performance goals
	GoalType_Optimize_PERFORMANCE = "optimize_performance"
	GoalType_Serivce_maintenance  = "service_maintenance"

	//documentation goals
	GoalType_Improve_DOCUMENTATION_READABILITY = "improve_documentation_readability"

	//test goals
	GoalType_Improve_TEST_COVERAGE = "improve_test_coverage"

	//code goals
	GoalType_Improve_CODE_QUALITY         = "improve_code_quality"
	GoalType_Improve_CODE_READABILITY     = "improve_code_readability"
	GoalType_Improve_CODE_MAINTAINABILITY = "improve_code_maintainability"
	GoalType_Improve_CODE_SECURITY        = "improve_code_security"
	GoalType_Improve_CODE_PERFORMANCE     = "improve_code_performance"
	GoalType_Improve_CODE_RELIABILITY     = "improve_code_reliability"
)
