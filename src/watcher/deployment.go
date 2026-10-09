package watcher

import (
	"fmt"
	"strings"
)

const (
	UntilSucceeded = "succeeded"
	UntilFailed    = "failed"

	sourceDeployment        = "deployment"
	typeDeploymentSucceeded = "deployment.succeeded"
	typeDeploymentFailed    = "deployment.failed"
	typeDeploymentUpdated   = "deployment.updated"
)

// DeploymentProbe watches one deployment/pipeline. Snapshot is how the host
// reads it (typically the deployment HTTP observer). The probe knows the
// object's vocabulary — pending / running / succeeded / failed — not the
// control plane's client.
type DeploymentProbe struct {
	Snapshot func(target string) (Observation, error)
}

func (DeploymentProbe) Kind() string { return KindDeployment }

func (DeploymentProbe) DefaultUntil() string { return UntilSucceeded }

func (p DeploymentProbe) Observe(spec Spec) (Observation, error) {
	if p.Snapshot == nil {
		return nil, fmt.Errorf("watcher: deployment probe has no snapshot")
	}
	return p.Snapshot(spec.Target)
}

func (DeploymentProbe) ID(spec Spec, obs Observation) string {
	id := firstNonEmpty(obs["deployment"], obs["target"], spec.Target)
	return KindDeployment + ":" + strings.ToLower(id)
}

func (DeploymentProbe) Fingerprint(obs Observation) string {
	return strings.ToLower(strings.TrimSpace(obs["state"])) + "|" + strings.TrimSpace(obs["healthy"])
}

func (DeploymentProbe) Done(obs Observation, until string) bool {
	state := strings.ToLower(strings.TrimSpace(obs["state"]))
	succeeded := state == UntilSucceeded
	failed := state == UntilFailed
	switch strings.ToLower(strings.TrimSpace(until)) {
	case UntilFailed:
		return failed
	case UntilTerminal:
		return succeeded || failed
	default:
		// until=succeeded: a failed pipeline will not succeed; stop watching.
		return succeeded || failed
	}
}

func (DeploymentProbe) Change(spec Spec, obs Observation) Change {
	id := firstNonEmpty(obs["deployment"], spec.Target)
	state := strings.ToLower(strings.TrimSpace(obs["state"]))
	typ := typeDeploymentUpdated
	suffix := "updated"
	switch state {
	case UntilSucceeded:
		typ = typeDeploymentSucceeded
		suffix = UntilSucceeded
	case UntilFailed:
		typ = typeDeploymentFailed
		suffix = UntilFailed
	}
	until := spec.Until
	if until == "" {
		until = UntilSucceeded
	}
	return Change{
		Source:         sourceDeployment,
		Type:           typ,
		TaskID:         spec.TaskID,
		AssetID:        "deployment:" + id,
		Payload:        observationPayload(obs, until),
		IdempotencyKey: "deployment:" + id + ":" + suffix,
	}
}
