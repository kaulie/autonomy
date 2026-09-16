package software_development

import (
	"strings"
	"testing"
)

// TestResolveDeploymentIdentityPrecedence pins the one order identity is read
// in: what the caller named, then the environment, then autonomy's own default —
// so an explicit caller always wins, an operator's process-wide setting comes
// next, and an unconfigured runtime still attributes the deploy to itself.
func TestResolveDeploymentIdentityPrecedence(t *testing.T) {
	t.Setenv(EnvIdentityRole, "user")
	t.Setenv(EnvIdentityID, "user_001")

	cases := []struct {
		name string
		in   map[string]string
		want deploymentIdentity
	}{
		{name: "default from env", in: map[string]string{}, want: deploymentIdentity{Role: "user", ID: "user_001"}},
		{name: "input role beats env, env id kept",
			in:   map[string]string{"identity_role": "agent"},
			want: deploymentIdentity{Role: "agent", ID: "user_001"}},
		{name: "input id beats env",
			in:   map[string]string{"identity_id": "agent_002"},
			want: deploymentIdentity{Role: "user", ID: "agent_002"}},
		{name: "role is trimmed and lowercased",
			in:   map[string]string{"identity_role": " Agent "},
			want: deploymentIdentity{Role: "agent", ID: "user_001"}},
		{name: "id is trimmed",
			in:   map[string]string{"identity_id": " agent_002 "},
			want: deploymentIdentity{Role: "user", ID: "agent_002"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDeploymentIdentity(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestResolveDeploymentIdentityDefaultsWithNothingSet: with neither input nor
// environment, autonomy's own identity is used — never an empty header pair.
func TestResolveDeploymentIdentityDefaultsWithNothingSet(t *testing.T) {
	t.Setenv(EnvIdentityRole, "")
	t.Setenv(EnvIdentityID, "")

	got, err := resolveDeploymentIdentity(map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Role != DefaultIdentityRole || got.ID != DefaultIdentityID || got.String() != "agent:autonomy" {
		t.Fatalf("got %+v (%q), want %s:%s", got, got.String(), DefaultIdentityRole, DefaultIdentityID)
	}
}

// TestResolveDeploymentIdentityRejectsWhatTheControlPlaneWould: the phase-1 rule
// is enforced where the value is set, with a message naming the offending value,
// instead of being sent and refused with a 401.
func TestResolveDeploymentIdentityRejectsWhatTheControlPlaneWould(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]string
		wantErr string
	}{
		{name: "unknown role", in: map[string]string{"identity_role": "robot"}, wantErr: "identity_role"},
		{name: "id too long", in: map[string]string{"identity_id": strings.Repeat("a", maxIdentityIDLen+1)}, wantErr: "too long"},
		{name: "id with whitespace", in: map[string]string{"identity_id": "user 001"}, wantErr: "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvIdentityRole, "")
			t.Setenv(EnvIdentityID, "")
			_, err := resolveDeploymentIdentity(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestResolveDeploymentIdentityTreatsBlankAsUnset: a whitespace-only value is
// "not said", not an invalid one — it falls through to the next source (here the
// default), exactly as an omitted key does.
func TestResolveDeploymentIdentityTreatsBlankAsUnset(t *testing.T) {
	t.Setenv(EnvIdentityRole, " ")
	t.Setenv(EnvIdentityID, "  ")

	got, err := resolveDeploymentIdentity(map[string]string{"identity_role": "", "identity_id": "\t"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Role != DefaultIdentityRole || got.ID != DefaultIdentityID {
		t.Fatalf("got %+v, want the default %s:%s", got, DefaultIdentityRole, DefaultIdentityID)
	}
}

// TestDeploymentIdentityValidateMirrorsTheControlPlane: validate() is the
// control plane's phase-1 rule stated next to the call, including the empty id a
// resolved identity can only reach if the default is ever emptied.
func TestDeploymentIdentityValidateMirrorsTheControlPlane(t *testing.T) {
	cases := []struct {
		name    string
		id      deploymentIdentity
		wantErr string
	}{
		{name: "user", id: deploymentIdentity{Role: "user", ID: "user_001"}},
		{name: "agent", id: deploymentIdentity{Role: "agent", ID: "agent_002"}},
		{name: "empty", id: deploymentIdentity{}, wantErr: "identity_role"},
		{name: "unknown role", id: deploymentIdentity{Role: "robot", ID: "r1"}, wantErr: "identity_role"},
		{name: "empty id", id: deploymentIdentity{Role: "agent", ID: ""}, wantErr: "identity_id is empty"},
		{name: "id too long", id: deploymentIdentity{Role: "agent", ID: strings.Repeat("a", maxIdentityIDLen+1)}, wantErr: "too long"},
		{name: "id with whitespace", id: deploymentIdentity{Role: "agent", ID: "agent 002"}, wantErr: "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.id.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestDeploymentIdentityHeadersAreTheContractItSends: the two header names are
// what the control plane reads, verbatim.
func TestDeploymentIdentityHeadersAreTheContractItSends(t *testing.T) {
	if identityRoleHeader != "identity_role" || identityIDHeader != "identity_id" {
		t.Fatalf("headers = %q / %q, want identity_role / identity_id", identityRoleHeader, identityIDHeader)
	}
	if EnvIdentityRole != "IDENTITY_ROLE" || EnvIdentityID != "IDENTITY_ID" {
		t.Fatalf("env = %q / %q, want IDENTITY_ROLE / IDENTITY_ID", EnvIdentityRole, EnvIdentityID)
	}
	if DefaultIdentityRole != "agent" || DefaultIdentityID != "autonomy" {
		t.Fatalf("defaults = %q:%q, want agent:autonomy", DefaultIdentityRole, DefaultIdentityID)
	}
}
