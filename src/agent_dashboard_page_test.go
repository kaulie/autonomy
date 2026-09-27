package autonomy

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestAgentDashboardTemplateParseError(t *testing.T) {
	bad := fstest.MapFS{
		"dashboard/templates/dashboard.html": {Data: []byte("{{ .Broken")},
	}
	if _, err := parseAgentDashboardTemplates(bad); err == nil {
		t.Fatal("parseAgentDashboardTemplates with invalid syntax: want error")
	}
}

func TestAgentDashboardTemplateMissingFile(t *testing.T) {
	empty := fstest.MapFS{}
	if _, err := parseAgentDashboardTemplates(empty); err == nil {
		t.Fatal("parseAgentDashboardTemplates with no templates: want error")
	}
}

func TestWriteAgentDashboard(t *testing.T) {
	var buf bytes.Buffer
	if err := writeAgentDashboard(&buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{"<!DOCTYPE html>", "Agent Status", "/api/agents", "setInterval", "<table"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered dashboard missing %q", want)
		}
	}
}

func TestAgentDashboardEmbeddedTemplates(t *testing.T) {
	if _, err := fs.Stat(agentDashboardTemplateFS, "dashboard/templates/dashboard.html"); err != nil {
		t.Fatalf("embedded dashboard template: %v", err)
	}
}
