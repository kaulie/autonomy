package autonomy

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability/deployment"
	sd "github.com/kaulie/autonomy/src/capability/software_development"
	"github.com/kaulie/autonomy/src/eventgateway"
	"github.com/kaulie/autonomy/src/watcher"
)

const (
	// EnvWatchInterval is the background poll interval for watch (seconds).
	EnvWatchInterval = "AUTONOMY_WATCH_INTERVAL"
	// EnvPRWatchInterval is the previous name; still read when the new one is unset.
	EnvPRWatchInterval = "AUTONOMY_PR_WATCH_INTERVAL"
)

// WatchView is one active background watch, as HTTP lists it.
type WatchView struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Target   string            `json:"target"`
	Until    string            `json:"until"`
	TaskID   string            `json:"task_id,omitempty"`
	Watching bool              `json:"watching"`
	Snapshot map[string]string `json:"snapshot,omitempty"`
}

// WatchRequest is POST /api/watches: start watching one world object.
// kind + target name it; pr / deployment are aliases that fill both.
type WatchRequest struct {
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	PR         string `json:"pr"`
	Deployment string `json:"deployment"`
	Until      string `json:"until"`
	TaskID     string `json:"task_id"`
}

// ListWatchesResponse is GET /api/watches.
type ListWatchesResponse struct {
	Watches []WatchView `json:"watches"`
	Count   int         `json:"count"`
}

func newWorldWatcher() *watcher.Watcher {
	return watcher.New([]watcher.Probe{
		watcher.PullRequestProbe{Snapshot: defaultPullRequestSnapshot},
		watcher.DeploymentProbe{Snapshot: defaultDeploymentSnapshot},
	}, watcher.WithInterval(watchInterval()))
}

func watchInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvWatchInterval))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(EnvPRWatchInterval))
	}
	if raw == "" {
		return watcher.DefaultInterval
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return watcher.DefaultInterval
	}
	return time.Duration(secs) * time.Second
}

func defaultPullRequestSnapshot(target string) (watcher.Observation, error) {
	out, err := sd.PRCheck{}.Run(map[string]string{"pr": target})
	if err != nil {
		return nil, err
	}
	return watcher.Observation(out), nil
}

func defaultDeploymentSnapshot(target string) (watcher.Observation, error) {
	snap, err := deployment.NewHTTPObserver().Observe(context.Background(), deployment.Request{Deployment: target})
	if err != nil {
		return nil, err
	}
	obs := watcher.Observation{
		"exists":     "true",
		"deployment": firstNonEmpty(snap.ID, target),
		"target":     firstNonEmpty(snap.ID, target),
		"state":      string(snap.State),
		"phase":      snap.Phase,
		"progress":   snap.Progress,
		"service":    snap.Service,
		"version":    snap.Version,
		"message":    snap.Message,
		"error":      snap.Error,
	}
	if snap.Healthy != nil {
		obs["healthy"] = strconv.FormatBool(*snap.Healthy)
	}
	return obs, nil
}

func (r *Autonomy) watchSink(ch watcher.Change) error {
	if r == nil {
		return fmt.Errorf("event gateway is off")
	}
	_, err := r.IngestWorldEvent(context.Background(), IngestEventRequest{
		Source: ch.Source,
		Type:   ch.Type,
		Subject: eventgateway.Subject{
			TaskID:  ch.TaskID,
			AssetID: ch.AssetID,
		},
		Payload:        ch.Payload,
		IdempotencyKey: ch.IdempotencyKey,
	})
	return err
}

func watchSpecFromRequest(req WatchRequest) (watcher.Spec, error) {
	kind := strings.TrimSpace(req.Kind)
	pr := strings.TrimSpace(req.PR)
	deploymentID := strings.TrimSpace(req.Deployment)
	target := firstNonEmpty(req.Target, pr, deploymentID)
	if kind == "" {
		switch {
		case pr != "":
			kind = watcher.KindPullRequest
		case deploymentID != "":
			kind = watcher.KindDeployment
		}
	}
	if kind == "" {
		return watcher.Spec{}, fmt.Errorf("kind is required")
	}
	if target == "" {
		return watcher.Spec{}, fmt.Errorf("target is required")
	}
	return watcher.Spec{
		Kind:   kind,
		Target: target,
		Until:  strings.TrimSpace(req.Until),
		TaskID: strings.TrimSpace(req.TaskID),
	}, nil
}

func watchView(id string, spec watcher.Spec, obs watcher.Observation, watching bool) WatchView {
	snapshot := map[string]string{}
	for k, v := range obs {
		snapshot[k] = v
	}
	return WatchView{
		ID:       id,
		Kind:     spec.Kind,
		Target:   firstNonEmpty(obs["target"], spec.Target),
		Until:    spec.Until,
		TaskID:   spec.TaskID,
		Watching: watching,
		Snapshot: snapshot,
	}
}

func watchID(w *watcher.Watcher, spec watcher.Spec, obs watcher.Observation) string {
	if w == nil {
		return spec.Kind + ":" + strings.ToLower(spec.Target)
	}
	for _, v := range w.List() {
		if v.Kind == spec.Kind && (v.Target == spec.Target || strings.EqualFold(v.Target, spec.Target)) {
			return v.ID
		}
	}
	// Not yet registered: mirror Probe.ID without reaching into the probe map.
	switch spec.Kind {
	case watcher.KindPullRequest:
		return watcher.PullRequestProbe{}.ID(spec, obs)
	case watcher.KindDeployment:
		return watcher.DeploymentProbe{}.ID(spec, obs)
	default:
		return spec.Kind + ":" + strings.ToLower(spec.Target)
	}
}

// StartWatch is HTTP's door onto watch: snapshot the object, and if it is not
// yet at until, register a background watch bound to task_id.
func (r *Autonomy) StartWatch(req WatchRequest) (*WatchView, error) {
	if r == nil || r.Watcher == nil {
		return nil, fmt.Errorf("watcher is off")
	}
	specIn, err := watchSpecFromRequest(req)
	if err != nil {
		return nil, err
	}
	obs, err := r.Watcher.Observe(specIn)
	if err != nil {
		return nil, err
	}
	specIn.Until = r.Watcher.Until(specIn)
	view := watchView(watchID(r.Watcher, specIn, obs), specIn, obs, false)
	if !presentObservation(obs) || r.Watcher.Terminal(specIn, obs) {
		return &view, nil
	}
	specIn.Fields = obs
	if err := r.Watcher.Watch(specIn); err != nil {
		return nil, err
	}
	view.Watching = true
	view.ID = watchID(r.Watcher, specIn, obs)
	return &view, nil
}

func (r *Autonomy) ListWatches() ListWatchesResponse {
	if r == nil || r.Watcher == nil {
		return ListWatchesResponse{Watches: []WatchView{}}
	}
	listed := r.Watcher.List()
	out := make([]WatchView, 0, len(listed))
	for _, v := range listed {
		out = append(out, WatchView{
			ID:       v.ID,
			Kind:     v.Kind,
			Target:   v.Target,
			Until:    v.Until,
			TaskID:   v.TaskID,
			Watching: v.Watching,
			Snapshot: v.Snapshot,
		})
	}
	return ListWatchesResponse{Watches: out, Count: len(out)}
}

func presentObservation(obs watcher.Observation) bool {
	if obs == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(obs["exists"])) {
	case "false", "0", "no":
		return false
	}
	return true
}
