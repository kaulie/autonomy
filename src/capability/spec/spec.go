// Package spec is what a capability declares about itself beyond its prose
// description: the inputs it takes and the outputs it returns.
//
// It is a leaf package on purpose. The parent package (src/capability) renders a
// capability's declaration into {{CONSTRUCTS}}, and the capabilities that make
// those declarations live in its subpackages — which the parent imports, so they
// cannot import it back. Both sides can import this one.
package spec

// Field is one input a capability takes or one output it returns: its name, the
// other names it answers to, whether a caller has to supply it, and what it is.
type Field struct {
	// Name is the canonical key; the aliases below are read as the same input.
	Name string `json:"name"`
	// Aliases are the other keys the capability reads for this field.
	Aliases []string `json:"aliases,omitempty"`
	// Required marks an input a plan step must supply (an output is never
	// required). A capability whose caller can name one of two things says so in
	// the description instead of marking both.
	Required bool `json:"required,omitempty"`
	// Description is one line on what the field is, and what an empty value means
	// when that is meaningful.
	Description string `json:"description"`
	// Kind is the typed object this field carries (an output) or accepts (an
	// input). Verification looks up kind → the one system tool that checks it.
	// Empty means the field is not a typed evidence object.
	Kind string `json:"kind,omitempty"`
}

// Object kinds that system verification tools check. A producer output and the
// tool input that receives it share the same kind.
const (
	KindPullRequest = "pull_request"
	KindDeployment  = "deployment"
)

// Declared is implemented by a capability that declares its call signature: the
// inputs it takes and the outputs it returns. The capability factory renders both
// into {{CONSTRUCTS}} (as "input" / "output"), so the planner — and a delegated
// worker reading the same list — can call it correctly instead of parsing
// Description() prose.
//
// It is optional, so a capability can be registered with only a description; every
// built-in declares its signature (see capability_test.go).
type Declared interface {
	Inputs() []Field
	Outputs() []Field
}

// SystemCheck marks a capability as a system verification tool for one object
// kind. The verifier looks up the evidence's kind and asks the tool that
// ChecksKind matches. Coverage grows by adding another tool — not by treating
// a worker report or a review as truth, and not by letting the planner pick.
type SystemCheck interface {
	ChecksKind() string
}

// WorkspaceRuntimeAssigned is the workspace fact a worker-acquiring capability
// reports to the runtime: AcquireAgent is called without a path, so that worker
// gets its own sandbox. Constructs omit this — each agent only concerns its own
// workspace.
const WorkspaceRuntimeAssigned = "runtime_assigned"

// WorkerScope is how a capability that acquires a worker tells the runtime
// where that worker runs. It is not rendered into Constructs.
type WorkerScope interface {
	WorkerWorkspace() string
}
