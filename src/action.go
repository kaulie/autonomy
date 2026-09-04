package autonomy

// Action is one concrete execution of a capability against a target.
type Action struct {
	Capability string
	Target     string
	Input      map[string]string
}
