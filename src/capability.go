package autonomy

// Capability is a named thing the system can do to or with the world.
type Capability interface {
	Name() string
	Run(in map[string]string) (map[string]string, error)
}
