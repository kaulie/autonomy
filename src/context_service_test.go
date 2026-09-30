package autonomy

import (
	"testing"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

// contextStoreStub is a Store that also serves the Context Service port.
type contextStoreStub struct {
	Store
	repo ctxsvc.Repository
}

func (s contextStoreStub) ContextRepository() ctxsvc.Repository { return s.repo }

// plainStoreStub is a Store that cannot serve the Context Service.
type plainStoreStub struct{ Store }

func TestBuildContextServiceUsesTheStorePort(t *testing.T) {
	repo := ctxsvc.NewMemoryRepository()
	svc := buildContextService(contextStoreStub{repo: repo})
	if svc == nil {
		t.Fatal("a store implementing ContextStore must yield a configured Context Service")
	}
	if got := buildContextService(plainStoreStub{}); got != nil {
		t.Fatalf("a store without the port must leave the service unconfigured, got %T", got)
	}
	if got := buildContextService(contextStoreStub{repo: nil}); got != nil {
		t.Fatalf("a store returning no repository must leave the service unconfigured, got %T", got)
	}
}
