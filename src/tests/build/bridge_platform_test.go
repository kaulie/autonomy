package build_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The release package carries the cursor bridge as a *downloaded, per-platform* binary.
// A build for another platform (the control plane injects GOOS/GOARCH when the deploy
// machine is a different platform) must therefore fetch the *target* platform's bridge:
// the build machine's copy is a different executable format and only fails later, on the
// target machine. These tests pin that contract without touching the network — the
// platform name is a pure function of the environment plus uname.

func script(name string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller")
	}
	// src/tests/build → repo root → scripts/<name>
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", name)
}

func repoFile(t *testing.T, rel ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller")
	}
	parts := append([]string{filepath.Dir(file), "..", "..", ".."}, rel...)
	body, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func printPlatform(t *testing.T, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", script("fetch-bridge.sh"), "--print-platform")
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestFetchBridgePlatformFollowsGoEnv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"go target wins", []string{"GOOS=linux", "GOARCH=amd64"}, "linux-x64"},
		{"darwin arm64", []string{"GOOS=darwin", "GOARCH=arm64"}, "darwin-arm64"},
		{"linux arm64", []string{"GOOS=linux", "GOARCH=arm64"}, "linux-arm64"},
		{"uname-style arch accepted", []string{"GOOS=linux", "GOARCH=x86_64"}, "linux-x64"},
		{"uname-style arm accepted", []string{"GOOS=linux", "GOARCH=aarch64"}, "linux-arm64"},
		{"GOARCH alone keeps host os", []string{"GOARCH=amd64"}, runtime.GOOS + "-x64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := printPlatform(t, tc.env...)
			if err != nil {
				t.Fatalf("%v (%s)", err, got)
			}
			if got != tc.want {
				t.Fatalf("--print-platform with %v = %q want %q", tc.env, got, tc.want)
			}
		})
	}
}

func TestFetchBridgePlatformDefaultsToBuildMachine(t *testing.T) {
	t.Parallel()
	got, err := printPlatform(t)
	if err != nil {
		t.Fatalf("%v (%s)", err, got)
	}
	if !strings.HasPrefix(got, runtime.GOOS+"-") {
		t.Fatalf("native --print-platform = %q, want a %s-* platform", got, runtime.GOOS)
	}
	if arch := strings.TrimPrefix(got, runtime.GOOS+"-"); arch != "x64" && arch != "arm64" {
		t.Fatalf("native --print-platform = %q, want x64 or arm64", got)
	}
}

func TestFetchBridgeRejectsUnsupportedPlatform(t *testing.T) {
	t.Parallel()
	for _, env := range [][]string{
		{"GOOS=plan9", "GOARCH=amd64"},
		{"GOOS=linux", "GOARCH=mips"},
	} {
		if got, err := printPlatform(t, env...); err == nil {
			t.Fatalf("--print-platform with %v succeeded with %q, want failure", env, got)
		}
	}
}

func TestFetchBridgeUrlIsPlatformParameterised(t *testing.T) {
	t.Parallel()
	text := repoFile(t, "scripts", "fetch-bridge.sh")
	for _, need := range []string{
		"${GOOS:-$(uname -s)}", // target platform comes from the caller first…
		"${GOARCH:-$(uname -m)}",
		"cursor-sdk-bridge-standalone-${OS}-${ARCH}.tar.gz", // …and picks the release asset
		"--print-platform",
	} {
		if !strings.Contains(text, need) {
			t.Errorf("scripts/fetch-bridge.sh no longer contains %q (cross-platform packaging breaks)", need)
		}
	}
}

func TestBuildScriptKeysBridgeCacheByTargetPlatform(t *testing.T) {
	t.Parallel()
	text := repoFile(t, "build.sh")
	for _, need := range []string{
		`--print-platform`,                                       // platform name comes from fetch-bridge.sh (one normalisation)
		`BUILD_PLATFORM="$(env -u GOOS -u GOARCH`,                // …and "the build machine" is asked with GOOS/GOARCH off
		`BRIDGE_CACHE="${BRIDGE_CACHE_ROOT}/${TARGET_PLATFORM}"`, // cache is per platform
	} {
		if !strings.Contains(text, need) {
			t.Errorf("build.sh no longer contains %q (a foreign bridge could be packaged again)", need)
		}
	}
	// The build machine's own copy (third_party/bin, used by dev runs and tests) is only a
	// valid source when the target platform *is* the build platform.
	guard := `if [ "${TARGET_PLATFORM}" = "${BUILD_PLATFORM}" ] && [ -x "${CHECKOUT_BRIDGE}" ]; then`
	if !strings.Contains(text, guard) {
		t.Error("build.sh may reuse third_party/bin/cursor-sdk-bridge for a foreign platform")
	}
	// …and it must not be overwritten by a foreign binary either.
	writeGuard := `if [ "${TARGET_PLATFORM}" = "${BUILD_PLATFORM}" ] && [ "${BRIDGE_SRC}" != "${CHECKOUT_BRIDGE}" ]; then`
	if !strings.Contains(text, writeGuard) {
		t.Error("build.sh may write a foreign bridge into third_party/bin/cursor-sdk-bridge")
	}
}
