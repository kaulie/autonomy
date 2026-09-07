package cursorsdk_test

import (
	"testing"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

func TestEndpointOptionsRequireBoth(t *testing.T) {
	c := cursorsdk.NewClient(cursorsdk.WithEndpoint("http://127.0.0.1:1", ""))
	if err := c.Ping(t.Context()); err == nil {
		t.Fatal("expected error when auth token missing")
	}
}
