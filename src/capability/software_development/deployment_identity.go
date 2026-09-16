package software_development

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// deploymentIdentity is who a deploy is attributed to, in the deployment control
// plane's phase-1 identity (see agent-control-plane-deployment README, the
// "phase-1 identity" section). It travels as two plain request headers on every
// deploy-triggering call, so a pipeline — and the deploy it enqueues — is
// attributable in the panel and the audit trail:
//
//	identity_role: user | agent
//	identity_id:   user_001 / agent_002 / ...
//
// There is no secret/token in phase 1: the control plane only refuses to act for
// a caller that did not identify itself (401, unless its IDENTITY_ENFORCE=0
// escape hatch is on). autonomy therefore never relies on that hatch — service.deploy
// always sends both headers.
type deploymentIdentity struct {
	Role string
	ID   string
}

const (
	// identityRoleHeader / identityIDHeader are the two headers the control
	// plane reads, by these exact names.
	identityRoleHeader = "identity_role"
	identityIDHeader   = "identity_id"

	// identityRoleUser / identityRoleAgent are the only roles phase 1 knows.
	identityRoleUser  = "user"
	identityRoleAgent = "agent"

	// EnvIdentityRole / EnvIdentityID override who autonomy's deploys are
	// attributed to, without a code change — the same variables the deployment
	// repo's own callers (bin/deploy.sh) read.
	EnvIdentityRole = "IDENTITY_ROLE"
	EnvIdentityID   = "IDENTITY_ID"

	// DefaultIdentityRole / DefaultIdentityID are who a deploy triggered by
	// autonomy is attributed to when nothing overrides it: the runtime is an
	// agent (not a person), named for the runtime that triggered the deploy.
	DefaultIdentityRole = identityRoleAgent
	DefaultIdentityID   = "autonomy"

	// maxIdentityIDLen mirrors the control plane's bound, so an over-long id is
	// rejected here — with the reason and the variable to fix — instead of coming
	// back as a 401 after a request was already made.
	maxIdentityIDLen = 64
)

// String renders the identity the way the control plane reports it ("role:id").
func (i deploymentIdentity) String() string { return i.Role + ":" + i.ID }

// resolveDeploymentIdentity decides who a deploy is attributed to, in one order:
// what the caller named (in["identity_role"] / in["identity_id"]), then the
// environment (IDENTITY_ROLE / IDENTITY_ID), then autonomy's own default. The
// role is lowercased like the control plane does, so "Agent" and "AGENT" are read
// as the one role phase 1 knows.
func resolveDeploymentIdentity(in map[string]string) (deploymentIdentity, error) {
	id := deploymentIdentity{
		Role: strings.ToLower(strings.TrimSpace(firstNonEmpty(in["identity_role"], os.Getenv(EnvIdentityRole), DefaultIdentityRole))),
		ID:   strings.TrimSpace(firstNonEmpty(in["identity_id"], os.Getenv(EnvIdentityID), DefaultIdentityID)),
	}
	if err := id.validate(); err != nil {
		return deploymentIdentity{}, err
	}
	return id, nil
}

// validate mirrors the control plane's phase-1 rule for the two headers, so a
// misconfigured value fails where it is set rather than as the control plane's
// 401. (The rule is deliberately duplicated: this is the contract this capability
// calls, and it belongs next to the call.)
func (i deploymentIdentity) validate() error {
	if i.Role != identityRoleUser && i.Role != identityRoleAgent {
		return fmt.Errorf("identity_role %q must be %q or %q (set %s)", i.Role, identityRoleUser, identityRoleAgent, EnvIdentityRole)
	}
	if i.ID == "" {
		return fmt.Errorf("identity_id is empty (set %s)", EnvIdentityID)
	}
	if len(i.ID) > maxIdentityIDLen {
		return fmt.Errorf("identity_id too long (max %d chars)", maxIdentityIDLen)
	}
	if strings.ContainsAny(i.ID, " \t\r\n") {
		return fmt.Errorf("identity_id %q must not contain whitespace", i.ID)
	}
	return nil
}

// setHeaders stamps the identity onto a deploy-triggering request.
func (i deploymentIdentity) setHeaders(req *http.Request) {
	req.Header.Set(identityRoleHeader, i.Role)
	req.Header.Set(identityIDHeader, i.ID)
}
