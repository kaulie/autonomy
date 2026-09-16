package software_development

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability/spec"
)

const (
	// DeployName is the capability's stable semantic name: deploy one service at
	// one branch, through the deployment control plane's pipeline.
	DeployName = "service.deploy"
	// DeployDomain is the semantic domain this capability belongs to.
	DeployDomain = "software_development"
	// DeployProvider is who carries the work out: the deployment control plane
	// (agent-control-plane), not autonomy itself. Like code_edit naming its
	// backend, the provider says where the effect really happens.
	DeployProvider = "agent-control-plane"

	// DefaultDeploymentAPIURL is where the local deployment control plane
	// listens. DEPLOYMENT_API_URL overrides it — the same variable the gateway
	// uses — so a non-default host/port needs no code change.
	DefaultDeploymentAPIURL = "http://127.0.0.1:4220"

	// deployNotifyPath is the control plane's "package this ref, then deploy it"
	// entry point (agent-control-plane POST /api/deploy-notify).
	deployNotifyPath = "/api/deploy-notify"

	// deployHTTPTimeout bounds the trigger call only. The pipeline itself is
	// asynchronous and runs for minutes, which is exactly why Run does not wait
	// for it.
	deployHTTPTimeout = 30 * time.Second

	// deployMaxResponseBytes bounds how much of the control plane's answer is
	// read into memory.
	deployMaxResponseBytes = 1 << 20
)

// DeployService triggers the deployment pipeline of one service at one branch:
// the deployment control plane packages that git ref (branch/tag) and deploys
// the result.
//
// It is deliberately fire-and-forget. Packaging → deploying → succeeded/failed
// takes minutes, so Run returns the pipeline id and its initial state as soon as
// the control plane accepts the request; watching a pipeline to its end is a
// later observation (GET /api/pipelines/<id>), not part of triggering it.
type DeployService struct {
	// APIURL overrides DEPLOYMENT_API_URL / the local default (tests, or an
	// explicit per-call host).
	APIURL string
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
}

func (DeployService) Name() string { return DeployName }

func (DeployService) Domain() string { return DeployDomain }

func (DeployService) Provider() string { return DeployProvider }

func (DeployService) Description() string {
	return `trigger the deployment pipeline of a service at a specific branch: the deployment control plane packages that git ref, then deploys it. input: {"service":"<service id>","branch":"<branch/tag>"} ("ref" works too; an empty branch means the service's default branch). Every call carries this runtime's deployment identity (identity_role/identity_id headers, default agent:autonomy; $IDENTITY_ROLE / $IDENTITY_ID override it) so the control plane records who triggered the deploy — no need to pass it. Returns as soon as the pipeline is accepted — it does not wait for packaging/deploying. output: {"pipeline_id":"<id>","state":"queued","service":"...","branch":"...","poll":"/api/pipelines/<id>"} (plus identity and deployment/version once the pipeline has them)`
}

// Inputs / Outputs declare the capability's call signature for {{CONSTRUCTS}}.
func (DeployService) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "service", Aliases: []string{"service_id"}, Required: true, Description: "the service to deploy, by the id the deployment control plane knows it as"},
		{Name: "branch", Aliases: []string{"ref"}, Description: "the git branch or tag to package; empty means the service's own default branch"},
		{Name: "identity_role", Description: `who the deploy is attributed to, "user" or "agent" (deployment control plane phase-1 identity header). Normally leave empty: empty means $IDENTITY_ROLE, then "agent"`},
		{Name: "identity_id", Description: `the caller's id, e.g. user_001 or agent_002 (header identity_id). Normally leave empty: empty means $IDENTITY_ID, then "autonomy" (this runtime's own identity)`},
	}
}

func (DeployService) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "pipeline_id", Description: "the accepted pipeline's id — poll it, do not wait for this call"},
		{Name: "state", Description: "the pipeline's state as accepted (queued)"},
		{Name: "branch", Description: "the ref the control plane actually packaged: the one named in the input, or the service's own default branch when the input left it empty"},
		{Name: "poll", Description: "the status path to follow, e.g. /api/pipelines/<id> (deployment.monitor takes it as poll)"},
		{Name: "deployment", Description: "the deployment the pipeline belongs to, once the control plane reports it"},
		{Name: "version", Description: "the version being deployed, once the control plane reports it"},
		{Name: "identity", Description: "who the control plane recorded as the triggerer, role:id (e.g. agent:autonomy)"},
	}
}

func (c DeployService) Run(in map[string]string) (map[string]string, error) {
	service := strings.TrimSpace(firstNonEmpty(in["service"], in["service_id"]))
	if service == "" {
		return nil, fmt.Errorf("service.deploy: missing service")
	}
	// An empty branch is meaningful, not missing: the control plane then packages
	// the service's own default branch. So it is passed through as-is (omitted).
	branch := strings.TrimSpace(firstNonEmpty(in["branch"], in["ref"]))

	// Who this deploy belongs to. Resolved before the request so a bad identity
	// (a typo'd $IDENTITY_ROLE, an over-long $IDENTITY_ID) fails here with the
	// reason, instead of the control plane's 401.
	identity, err := resolveDeploymentIdentity(in)
	if err != nil {
		return nil, fmt.Errorf("service.deploy: %w", err)
	}

	body, err := json.Marshal(deployNotifyRequest{ServiceID: service, Ref: branch})
	if err != nil {
		return nil, fmt.Errorf("service.deploy: encode request: %w", err)
	}
	url := strings.TrimRight(c.baseURL(), "/") + deployNotifyPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("service.deploy: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Phase-1 identity headers: every deploy-triggering call must say who
	// triggered it, or the control plane refuses with 401.
	identity.setHeaders(req)

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: deployHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("service.deploy: %s: %w", url, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, deployMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("service.deploy: read response: %w", err)
	}
	// 202 is what an accepted pipeline returns; any 2xx counts so the capability
	// does not read a future 200 as a failure.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("service.deploy: %s: %s: %s", url, resp.Status, deployErrorDetail(payload))
	}
	var job deployPipeline
	if err := json.Unmarshal(payload, &job); err != nil {
		return nil, fmt.Errorf("service.deploy: decode response: %w", err)
	}
	// No pipeline id means nothing was queued: reporting success here would hand
	// the planner a pipeline it can never poll.
	if strings.TrimSpace(job.RequestID) == "" {
		return nil, fmt.Errorf("service.deploy: %s: control plane accepted the request without a pipeline id", url)
	}

	out := map[string]string{
		"branch":      job.Ref,
		"pipeline_id": job.RequestID,
		"state":       job.State,
		"poll":        firstNonEmpty(job.Poll, "/api/pipelines/"+job.RequestID),
		// The control plane echoes what it recorded as the triggerer; fall back to
		// the identity this call carried when an older control plane does not.
		"identity": firstNonEmpty(job.TriggeredBy, identity.String()),
	}
	// The service is the step's own input, and the control plane's message about
	// accepting the request is prose — neither is something this call produced. What
	// only it can say is in `branch` (the ref it resolved), `deployment` and `version`.
	for k, v := range map[string]string{
		"deployment": job.Deployment,
		"version":    job.Version,
	} {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out, nil
}

// baseURL resolves where the deployment control plane listens: the explicit
// override, then DEPLOYMENT_API_URL, then the local default.
func (c DeployService) baseURL() string {
	if url := strings.TrimSpace(c.APIURL); url != "" {
		return url
	}
	if url := strings.TrimSpace(os.Getenv("DEPLOYMENT_API_URL")); url != "" {
		return url
	}
	return DefaultDeploymentAPIURL
}

// deployNotifyRequest is the body of POST /api/deploy-notify.
type deployNotifyRequest struct {
	ServiceID string `json:"serviceId"`
	// Ref is the git branch/tag to package. Empty lets the control plane use the
	// service's default branch, so it is omitted rather than sent as "".
	Ref string `json:"ref,omitempty"`
}

// deployPipeline is an accepted pipeline job as the control plane reports it.
type deployPipeline struct {
	RequestID       string `json:"requestId"`
	ServiceID       string `json:"serviceId"`
	Ref             string `json:"ref"`
	State           string `json:"state"`
	Deployment      string `json:"deployment"`
	Version         string `json:"version"`
	Message         string `json:"message"`
	Poll            string `json:"poll"`
	DeployRequestID string `json:"deployRequestId"`
	// TriggeredBy is the identity the control plane recorded for this pipeline
	// ("role:id"), echoed back so the caller can see who it attributed the deploy
	// to (phase-1 identity).
	TriggeredBy string `json:"triggeredBy"`
}

// deployErrorDetail pulls the control plane's own error message out of a failed
// response, so the planner sees the reason ("service not found: x") instead of
// only a status code.
func deployErrorDetail(payload []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(payload, &e); err == nil {
		if msg := strings.TrimSpace(e.Error); msg != "" {
			return msg
		}
	}
	if s := strings.TrimSpace(string(payload)); s != "" {
		return s
	}
	return "no response body"
}
