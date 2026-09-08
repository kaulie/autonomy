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

func TestRegisterDefaultsIncludesAssetChange(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	assets := memAssets{"1": "alive"}
	capability.RegisterDefaults(f, capability.Deps{Assets: assets})
	all := f.GetAll()
	if len(all) != 2 {
		t.Fatalf("GetAll len=%d want 2", len(all))
	}
	if f.Get("asset.change") == nil || f.Get("code_edit") == nil {
		t.Fatalf("GetAll=%v", all)
	}
	got := f.FormatConstructs()
	if !strings.Contains(got, "asset.change [provider=autonomy]") {
		t.Fatalf("constructs=%q", got)
	}
	if !strings.Contains(got, "code_edit [provider=cursor]") {
		t.Fatalf("expected code_edit in constructs: %q", got)
	}
	out, err := f.Get("asset.change").Run(map[string]string{"target": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "changed" {
		t.Fatalf("out=%v", out)
	}
}
