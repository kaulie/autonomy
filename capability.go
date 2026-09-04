package autonomy

// Capability is a named thing the system can do.
type Capability interface {
	Name() string
	Run(in map[string]string) (map[string]string, error)
}

// Step is the next capability invocation chosen by an agent.
type Step struct {
	Capability string
	Input      map[string]string
}
