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
}

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
