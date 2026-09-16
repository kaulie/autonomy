package software_development_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// pipelineStub stands in for the deployment control plane: it records the one
// request service.deploy makes and answers with the status/body a test asks for.
type pipelineStub struct {
	t           *testing.T
	status      int
	response    string
	calls       int32
	method      string
	path        string
	contentType string
	rawBody     string
	body        map[string]string
	identityRol string
	identityID  string
}

func newPipelineStub(t *testing.T, status int, response string) *pipelineStub {
	t.Helper()
	return &pipelineStub{t: t, status: status, response: response}
}

func (s *pipelineStub) start() *httptest.Server {
	s.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.calls, 1)
		s.method = r.Method
		s.path = r.URL.Path
		s.contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		s.rawBody = string(raw)
		s.identityRol = r.Header.Get("identity_role")
		s.identityID = r.Header.Get("identity_id")
		_ = json.Unmarshal(raw, &s.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.response))
	}))
	s.t.Cleanup(srv.Close)
	return srv
}

func (s *pipelineStub) callCount() int { return int(atomic.LoadInt32(&s.calls)) }

// TestServiceDeployTriggersThePipelineAtTheGivenBranch pins the trigger itself:
// POST /api/deploy-notify with {"serviceId","ref"} — the two things the caller
// names (service + branch, the branch being the ref the pipeline packages).
func TestServiceDeployTriggersThePipelineAtTheGivenBranch(t *testing.T) {
	stub := newPipelineStub(t, http.StatusAccepted,
		`{"requestId":"pipeline-1a2b3c4d","serviceId":"web-cursor","ref":"release/1.2","state":"queued",`+
			`"message":"accepted; package release/1.2 (latest) then deploy with graceful notify+poll",`+
			`"requestedAt":"2026-09-15T10:00:00Z","poll":"/api/pipelines/pipeline-1a2b3c4d"}`)
	srv := stub.start()

	out, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{
		"service": "web-cursor",
		"branch":  "release/1.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodPost || stub.path != "/api/deploy-notify" {
		t.Fatalf("request was %s %s, want POST /api/deploy-notify", stub.method, stub.path)
	}
	if got := stub.contentType; !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}
	if stub.body["serviceId"] != "web-cursor" || stub.body["ref"] != "release/1.2" {
		t.Fatalf("body=%s", stub.rawBody)
	}
	for k, want := range map[string]string{
		"pipeline_id": "pipeline-1a2b3c4d",
		"state":       "queued",
		"poll":        "/api/pipelines/pipeline-1a2b3c4d",
	} {
		if out[k] != want {
			t.Errorf("out[%q]=%q, want %q (out=%v)", k, out[k], want, out)
		}
	}
	// What was asked for is the step's input, and who triggered it is the control
	// plane's audit — neither is something this call produced.
	for _, notAResult := range []string{"service", "branch", "message", "identity"} {
		if _, ok := out[notAResult]; ok {
			t.Errorf("out=%v carries %q, which is about the call, not its product", out, notAResult)
		}
	}
}

// TestServiceDeployWithoutBranchLetsTheControlPlanePickTheDefault: an omitted
// branch is sent as no ref at all, so the control plane packages the service's
// own default branch; the branch that was really used comes back in the output.
func TestServiceDeployWithoutBranchLetsTheControlPlanePickTheDefault(t *testing.T) {
	stub := newPipelineStub(t, http.StatusAccepted,
		`{"requestId":"pipeline-9","serviceId":"web-cursor","ref":"main","state":"queued"}`)
	srv := stub.start()

	out, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{"service": "web-cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stub.rawBody, "ref") {
		t.Fatalf("empty branch must not be sent, body=%s", stub.rawBody)
	}
	if out["poll"] != "/api/pipelines/pipeline-9" {
		t.Errorf("poll=%q, want the poll path derived from the pipeline id", out["poll"])
	}
	// Which ref the control plane picked is its resolution of this call's input, not
	// something the call produced: the pipeline it created is what is reported.
	if _, ok := out["branch"]; ok {
		t.Errorf("out=%v, want no branch: the ref is the step's own input", out)
	}
}

// TestServiceDeployAcceptsRefAndServiceIDAliases: the planner may name the
// branch "ref" (the control plane's own word) and the service "service_id".
func TestServiceDeployAcceptsRefAndServiceIDAliases(t *testing.T) {
	stub := newPipelineStub(t, http.StatusAccepted, `{"requestId":"pipeline-7","state":"queued"}`)
	srv := stub.start()

	if _, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{
		"service_id": "agent-benchmark-tool",
		"ref":        "develop",
	}); err != nil {
		t.Fatal(err)
	}
	if stub.body["serviceId"] != "agent-benchmark-tool" || stub.body["ref"] != "develop" {
		t.Fatalf("body=%s", stub.rawBody)
	}
}

// TestServiceDeployRequiresAService: nothing is triggered without a service
// name — the capability fails before it makes a call.
func TestServiceDeployRequiresAService(t *testing.T) {
	stub := newPipelineStub(t, http.StatusAccepted, `{"requestId":"pipeline-1"}`)
	srv := stub.start()

	_, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{"branch": "main"})
	if err == nil || !strings.Contains(err.Error(), "missing service") {
		t.Fatalf("err=%v, want a missing-service error", err)
	}
	if stub.callCount() != 0 {
		t.Fatalf("called the control plane %d time(s) without a service", stub.callCount())
	}
}

// TestServiceDeploySurfacesTheControlPlaneError: the planner must see the
// control plane's own reason (e.g. an unregistered service), not just a status.
func TestServiceDeploySurfacesTheControlPlaneError(t *testing.T) {
	stub := newPipelineStub(t, http.StatusNotFound, `{"error":"service not found: nope"}`)
	srv := stub.start()

	_, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{"service": "nope"})
	if err == nil {
		t.Fatal("expected an error for a rejected pipeline")
	}
	for _, want := range []string{"404", "service not found: nope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err=%v, want it to mention %q", err, want)
		}
	}
}

// TestServiceDeployFailsWithoutAPipelineID: an accepted-looking answer with no
// pipeline id queued nothing, so reporting success would hand the planner a
// pipeline it can never poll.
func TestServiceDeployFailsWithoutAPipelineID(t *testing.T) {
	stub := newPipelineStub(t, http.StatusAccepted, `{"state":"queued"}`)
	srv := stub.start()

	_, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{"service": "web-cursor"})
	if err == nil || !strings.Contains(err.Error(), "without a pipeline id") {
		t.Fatalf("err=%v, want a missing-pipeline-id error", err)
	}
}

// TestServiceDeployReportsAnUnreachableControlPlane: a dead control plane is a
// failed trigger that names where it tried to reach.
func TestServiceDeployReportsAnUnreachableControlPlane(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := (sd.DeployService{APIURL: url}).Run(map[string]string{"service": "web-cursor"})
	if err == nil || !strings.Contains(err.Error(), "/api/deploy-notify") {
		t.Fatalf("err=%v, want a connection error naming the endpoint", err)
	}
}

// TestServiceDeployUsesDeploymentAPIURL: DEPLOYMENT_API_URL (the variable the
// gateway already uses) is how a pipeline is triggered on another host/port.
func TestServiceDeployUsesDeploymentAPIURL(t *testing.T) {
	stub := newPipelineStub(t, http.StatusAccepted, `{"requestId":"pipeline-3","state":"queued"}`)
	srv := stub.start()
	t.Setenv("DEPLOYMENT_API_URL", srv.URL)

	if _, err := (sd.DeployService{}).Run(map[string]string{"service": "web-cursor"}); err != nil {
		t.Fatal(err)
	}
	if stub.callCount() != 1 {
		t.Fatalf("control plane called %d time(s), want 1", stub.callCount())
	}
}

// TestServiceDeployAlwaysCarriesItsIdentityHeaders: every deploy-triggering call
// names who triggered it, in the deployment control plane's phase-1 identity
// headers (see agent-control-plane-deployment README) — by default the
// runtime's own agent identity, so a deploy is attributable even when nobody
// configured anything.
func TestServiceDeployAlwaysCarriesItsIdentityHeaders(t *testing.T) {
	t.Setenv(sd.EnvIdentityRole, "")
	t.Setenv(sd.EnvIdentityID, "")
	stub := newPipelineStub(t, http.StatusAccepted, `{"requestId":"pipeline-5","state":"queued"}`)
	srv := stub.start()

	out, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{"service": "web-cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if stub.identityRol != sd.DefaultIdentityRole || stub.identityID != sd.DefaultIdentityID {
		t.Fatalf("headers identity_role=%q identity_id=%q, want %q / %q",
			stub.identityRol, stub.identityID, sd.DefaultIdentityRole, sd.DefaultIdentityID)
	}
	// The attribution goes with the call; the output is the pipeline it created.
	if _, ok := out["identity"]; ok {
		t.Errorf("out=%v, want no identity: who triggered it is the control plane's audit", out)
	}
}

// TestServiceDeployIdentityComesFromTheEnvironment: an operator can decide who
// autonomy's deploys are attributed to (IDENTITY_ROLE / IDENTITY_ID), the same
// variables the deployment repo's own callers read — no code change.
func TestServiceDeployIdentityComesFromTheEnvironment(t *testing.T) {
	t.Setenv(sd.EnvIdentityRole, "user")
	t.Setenv(sd.EnvIdentityID, "user_001")
	stub := newPipelineStub(t, http.StatusAccepted, `{"requestId":"pipeline-6","state":"queued"}`)
	srv := stub.start()

	_, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{"service": "web-cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if stub.identityRol != "user" || stub.identityID != "user_001" {
		t.Fatalf("headers identity_role=%q identity_id=%q, want user / user_001", stub.identityRol, stub.identityID)
	}
}

// TestServiceDeployIdentityInputBeatsTheEnvironment: a plan step (or a task) can
// attribute one deploy to a specific identity, and it wins over the process-wide
// default. The role is read case-insensitively, like the control plane does.
func TestServiceDeployIdentityInputBeatsTheEnvironment(t *testing.T) {
	t.Setenv(sd.EnvIdentityRole, "user")
	t.Setenv(sd.EnvIdentityID, "user_001")
	stub := newPipelineStub(t, http.StatusAccepted, `{"requestId":"pipeline-7","state":"queued"}`)
	srv := stub.start()

	_, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{
		"service":       "web-cursor",
		"identity_role": " Agent ",
		"identity_id":   "agent_002",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.identityRol != "agent" || stub.identityID != "agent_002" {
		t.Fatalf("headers identity_role=%q identity_id=%q, want agent / agent_002", stub.identityRol, stub.identityID)
	}
}

// TestServiceDeployRejectsAnUnusableIdentityBeforeCalling: a misconfigured
// identity (a role phase 1 does not know) fails where it is set, rather than
// going out and coming back as the control plane's 401.
func TestServiceDeployRejectsAnUnusableIdentityBeforeCalling(t *testing.T) {
	t.Setenv(sd.EnvIdentityRole, "robot")
	stub := newPipelineStub(t, http.StatusAccepted, `{"requestId":"pipeline-8","state":"queued"}`)
	srv := stub.start()

	_, err := (sd.DeployService{APIURL: srv.URL}).Run(map[string]string{"service": "web-cursor"})
	if err == nil || !strings.Contains(err.Error(), "identity_role") {
		t.Fatalf("err=%v, want it to name the bad identity_role", err)
	}
	if stub.callCount() != 0 {
		t.Fatalf("called the control plane %d time(s) with an unusable identity", stub.callCount())
	}
}

// TestServiceDeployMetadataPinsTheSemantics: the name/domain/provider the
// planner sees, and the one thing the description must not leave ambiguous —
// that Run returns as soon as the pipeline is accepted instead of waiting for
// it. The default target is a constant check on purpose: calling the default
// would queue a real deployment.
func TestServiceDeployMetadataPinsTheSemantics(t *testing.T) {
	c := sd.DeployService{}
	if c.Name() != sd.DeployName || sd.DeployName != "service.deploy" {
		t.Errorf("name=%q/%q", c.Name(), sd.DeployName)
	}
	if c.Domain() != sd.DeployDomain || sd.DeployDomain != "software_development" {
		t.Errorf("domain=%q/%q", c.Domain(), sd.DeployDomain)
	}
	if c.Provider() != sd.DeployProvider || sd.DeployProvider != "agent-control-plane" {
		t.Errorf("provider=%q/%q", c.Provider(), sd.DeployProvider)
	}
	if sd.DefaultDeploymentAPIURL != "http://127.0.0.1:4220" {
		t.Errorf("default deployment API=%q", sd.DefaultDeploymentAPIURL)
	}
	for _, want := range []string{"service", "branch", "pipeline_id", "does not wait", "identity"} {
		if !strings.Contains(c.Description(), want) {
			t.Errorf("description missing %q: %s", want, c.Description())
		}
	}
}
