package autonomy

import (
	"context"
	"testing"

	ctxsvc "github.com/kaulie/autonomy/src/context"
	"github.com/kaulie/autonomy/src/knowledgebase"
)

func TestBuildContextServiceReadsKnowledgeBase(t *testing.T) {
	svc := buildContextService()
	if svc == nil {
		t.Fatal("context reads must be wired to knowledge-base")
	}
	client, ok := svc.(*knowledgebase.Client)
	if !ok {
		t.Fatalf("got %T, want *knowledgebase.Client", svc)
	}
	if client.BaseURL() != knowledgebase.DefaultURL {
		t.Fatalf("base URL = %s", client.BaseURL())
	}
	if _, err := svc.Register(context.Background(), ctxsvc.Resource{ProjectID: "p"}); ctxsvc.CodeOf(err) != ctxsvc.ErrIndexFailed {
		t.Fatalf("writes must stay on knowledge-base, got %v", err)
	}
}
