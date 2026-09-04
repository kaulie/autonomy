package autonomy

// Task is the work contract: what to achieve and how completion is judged.
// It defines goal, not path.
type Task struct {
	ID       string
	Domain   string
	Context  string
	Target   string
	Goal     string
	Contract Contract
}

// Contract is the completion anchor. Agents may choose any path; they may not rewrite this.
type Contract struct {
	ExpectedState string
}
