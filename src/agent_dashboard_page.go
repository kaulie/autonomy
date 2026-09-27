package autonomy

import (
	"embed"
	"html/template"
	"io"
	"io/fs"
)

// The agent-status monitoring page served at GET /dashboard (src/http_server.go).
// Markup lives in dashboard/templates/*.html, embedded at build time and parsed once
// at startup. The page fetches GET /api/agents on a timer client-side.

//go:embed dashboard/templates/*.html
var agentDashboardTemplateFS embed.FS

const agentDashboardTemplateName = "dashboard.html"

// AgentDashboardPageData is the view model for GET /dashboard. Agent rows are loaded
// in the browser from /api/agents; the server only renders the static shell.
type AgentDashboardPageData struct{}

func parseAgentDashboardTemplates(fsys fs.FS) (*template.Template, error) {
	return template.ParseFS(fsys, "dashboard/templates/*.html")
}

var agentDashboardTemplates = template.Must(parseAgentDashboardTemplates(agentDashboardTemplateFS))

func writeAgentDashboard(w io.Writer) error {
	return agentDashboardTemplates.ExecuteTemplate(w, agentDashboardTemplateName, AgentDashboardPageData{})
}
