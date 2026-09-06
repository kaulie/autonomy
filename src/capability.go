package autonomy

// Capability is a named thing the system can do to or with the world.
type Capability interface {
	Name() string
	Run(in map[string]string) (map[string]string, error)
}

type CapabilityFactory struct {
	capabilities       []Capability
	capabilitiesByName map[string]Capability
}

func (f *CapabilityFactory) GetAll() []Capability {
	return f.capabilities
}

func NewCapabilityFactory() *CapabilityFactory {
	return &CapabilityFactory{
		capabilities:       make([]Capability, 0),
		capabilitiesByName: make(map[string]Capability),
	}
}

func (f *CapabilityFactory) Register(capability Capability) {
	f.capabilitiesByName[capability.Name()] = capability
}

func (f *CapabilityFactory) Get(name string) Capability {
	return f.capabilitiesByName[name]
}
