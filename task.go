package autonomy

// Task is the work contract: what to achieve and how completion is judged.
type Task struct {
	ID       string
	Goal     string
	Context  string
	Asset    string
	Contract Contract
}

// Contract is the completion anchor. Agents may choose any path; they may not rewrite this.
type Contract struct {
	ExpectedState string
}
