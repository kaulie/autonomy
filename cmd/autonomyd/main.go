// Command autonomyd runs the Autonomy runtime: the process that owns the store,
// the agents and the world, and serves the task HTTP API (docs/http-api.md).
//
// It is this repository's own entry into the library — cmd/autonomy and every
// other caller talk to it over HTTP, not over Go. The deployment platform starts
// this binary: build.sh builds it to bin/autonomyd, and scripts/start.sh runs it
// with AUTONOMY_HTTP_ADDR set from SERVICE_PORT.
//
//	go run ./cmd/autonomyd                                  # serves on :4230
//	AUTONOMY_HTTP_ADDR=127.0.0.1:4230 go run ./cmd/autonomyd
//
// Its client counterpart is cmd/autonomy:
//
//	go run ./cmd/autonomy -description "开放服务契约的前端入口"
package main

import (
	"fmt"
	"os"
	"strings"

	autonomy "github.com/kaulie/autonomy/src"
)

// version is stamped by build.sh (-X main.version=$APP_VERSION).
var version = "dev"

// defaultAddr is where the runtime listens when AUTONOMY_HTTP_ADDR is not set.
// A deployment always sets it — scripts/start.sh derives it from SERVICE_PORT.
const defaultAddr = ":4230"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	addr := strings.TrimSpace(os.Getenv("AUTONOMY_HTTP_ADDR"))
	if addr == "" {
		addr = defaultAddr
	}

	runtime, err := autonomy.BootstrapAutonomy()
	if err != nil {
		return err
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	// The world tasks run in — the demo one the documented instruction names.
	// It is seeded here because here is where tasks run: a caller can reference
	// context over HTTP (context_ref), but registering it is not something the
	// API takes.
	if err := seedDemoWorld(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "[autonomy] version=%s listen=%s\n", version, addr)
	return autonomy.NewHTTPServer(runtime).ListenAndServe(addr)
}
