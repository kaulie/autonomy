// Package capability holds the Capability interface, factory, and built-in abilities.
// Domain-specific abilities live under subpackages (e.g. software_development).
package capability

import (
	"encoding/json"
)

// Capability is a named thing the system can do to or with the world.
type Capability interface {
	Name() string
	Domain() string
	Provider() string // who provides this capability (e.g. cursor, autonomy)
	Description() string
	Run(in map[string]string) (map[string]string, error)
}

// Factory creates and caches capabilities by name.
type Factory struct {
	capabilities       []Capability
	capabilitiesByName map[string]Capability
}

func NewFactory() *Factory {
	return &Factory{
		capabilities:       make([]Capability, 0),
		capabilitiesByName: make(map[string]Capability),
	}
}

func (f *Factory) GetAll() []Capability {
	return f.capabilities
}

func (f *Factory) Register(c Capability) {
	name := c.Name()
	if _, exists := f.capabilitiesByName[name]; exists {
		for i, existing := range f.capabilities {
			if existing.Name() == name {
				f.capabilities[i] = c
				break
			}
		}
	} else {
		f.capabilities = append(f.capabilities, c)
	}
	f.capabilitiesByName[name] = c
}

func (f *Factory) Get(name string) Capability {
	return f.capabilitiesByName[name]
}

// Has reports whether a capability name is registered.
func (f *Factory) Has(name string) bool {
	if f == nil {
		return false
	}
	_, ok := f.capabilitiesByName[name]
	return ok
}

// FormatConstructs renders registered capabilities for AGENT_V2 {{CONSTRUCTS}}
// as a JSON array.
func (f *Factory) FormatConstructs() string {
	if f == nil || len(f.capabilities) == 0 {
		return "[]"
	}
	type constructJSON struct {
		Name        string `json:"name"`
		Domain      string `json:"domain"`
		Provider    string `json:"provider"`
		Description string `json:"description"`
	}
	out := make([]constructJSON, 0, len(f.capabilities))
	for _, c := range f.capabilities {
		out = append(out, constructJSON{
			Name:        c.Name(),
			Domain:      c.Domain(),
			Provider:    c.Provider(),
			Description: c.Description(),
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(b)
}
