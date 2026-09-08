package autonomy

import (
	"fmt"
	"strings"
)

// Capability is a named thing the system can do to or with the world.
type Capability interface {
	Name() string
	Domain() TaskDomain
	Description() string
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
	name := capability.Name()
	if _, exists := f.capabilitiesByName[name]; exists {
		for i, c := range f.capabilities {
			if c.Name() == name {
				f.capabilities[i] = capability
				break
			}
		}
	} else {
		f.capabilities = append(f.capabilities, capability)
	}
	f.capabilitiesByName[name] = capability
}

func (f *CapabilityFactory) Get(name string) Capability {
	return f.capabilitiesByName[name]
}

// FormatConstructs renders registered capabilities for AGENT_V1 {{CONSTRUCTS}}.
func (f *CapabilityFactory) FormatConstructs() string {
	if f == nil || len(f.capabilities) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, c := range f.capabilities {
		fmt.Fprintf(&b, "- %s: %s\n", c.Name(), c.Description())
	}
	return strings.TrimSpace(b.String())
}

// AssetChangeCapability mutates the task target asset toward Contract.ExpectedState.
type AssetChangeCapability struct{}

func (AssetChangeCapability) Name() string { return "asset.change" }

func (AssetChangeCapability) Domain() TaskDomain { return TaskDomainServer }

func (AssetChangeCapability) Description() string {
	return `mutate the task target asset toward Contract.ExpectedState. input: {"target":"<asset id>"} (optional; defaults to task Target)`
}

func (AssetChangeCapability) Run(in map[string]string) (map[string]string, error) {
	target := strings.TrimSpace(in["target"])
	if target == "" {
		return nil, fmt.Errorf("asset.change: missing target")
	}
	if _world == nil || _world.assetManager == nil {
		return nil, fmt.Errorf("asset.change: world not ready")
	}
	asset, err := _world.assetManager.Get(target)
	if err != nil {
		return nil, err
	}
	asset.State = "changed"
	_world.assetManager.Set(target, asset)
	return map[string]string{"target": target, "state": asset.State}, nil
}
