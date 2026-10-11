package claude

import (
	"context"
	"slices"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestClaudeCatalogueOffersTheCLIAliases(t *testing.T) {
	vendors, err := claudeVendors(context.Background(), llmbackend.Creds{})
	if err != nil || !slices.Equal(vendors, []string{"anthropic"}) {
		t.Fatalf("vendors=%v err=%v want anthropic", vendors, err)
	}
	models, err := claudeModels(context.Background(), llmbackend.Creds{})
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	for _, want := range []string{"sonnet", "opus", "haiku"} {
		if !slices.Contains(models, want) {
			t.Fatalf("models=%v want %q so a create-task picker has something to offer", models, want)
		}
	}
}
