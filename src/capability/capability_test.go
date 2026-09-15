package capability_test

import (
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
)

type memAssets map[string]string

func (m memAssets) GetState(id string) (string, error) {
	s, ok := m[id]
	if !ok {
		return "", errNotFound(id)
	}
	return s, nil
}

func (m memAssets) SetState(id, state string) error {
	if _, ok := m[id]; !ok {
		return errNotFound(id)
	}
	m[id] = state
	return nil
}

type errNotFound string

func (e errNotFound) Error() string { return "asset " + string(e) + " not found" }

func TestRegisterDefaultsIncludesBuiltins(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	assets := memAssets{"1": "alive"}
	capability.RegisterDefaults(f, capability.Deps{Assets: assets})
	all := f.GetAll()
	if len(all) != 3 {
		t.Fatalf("GetAll len=%d want 3", len(all))
	}
	for _, name := range []string{"asset.change", "code_edit", "service.deploy"} {
		if f.Get(name) == nil {
			t.Fatalf("%s not registered: %v", name, all)
		}
	}
	got := f.FormatConstructs()
	if !strings.Contains(got, `"name": "asset.change"`) {
		t.Fatalf("constructs=%q", got)
	}
	if !strings.Contains(got, `"name": "code_edit"`) {
		t.Fatalf("expected code_edit in constructs: %q", got)
	}
	// service.deploy is planner-visible like any other construct: the planner
	// learns it can trigger a deployment pipeline from the same list.
	if !strings.Contains(got, `"name": "service.deploy"`) {
		t.Fatalf("expected service.deploy in constructs: %q", got)
	}
	out, err := f.Get("asset.change").Run(map[string]string{"target": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "changed" {
		t.Fatalf("out=%v", out)
	}
}
