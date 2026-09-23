// Command autonomyd runs the Autonomy runtime: the process that owns the store,
// the agents and the world, and serves the task HTTP API (docs/http-api.md).
//
// It is this repository's own entry into the library — cmd/autonomy and every
// other caller talk to it over HTTP, not over Go. The deployment platform starts
// this binary: build.sh builds it to bin/autonomyd, and scripts/start.sh runs it
// with AUTONOMY_HTTP_ADDR set from SERVICE_PORT.
//
//	go run ./cmd/autonomyd                                  # serves on :4300
//	AUTONOMY_HTTP_ADDR=127.0.0.1:4300 go run ./cmd/autonomyd
//
// Its client counterpart is cmd/autonomy:
//
//	go run ./cmd/autonomy -description "开放服务契约的前端入口"
//	go run ./cmd/autonomy -broadcast all -description "今天 18:00 全员停服演练"
//
// General API Info for swaggo/swag — this comment block is the annotation entry
// point the release step reads (`swag init -g cmd/autonomyd/main.go`, then
// scripts/register-contract.sh registers the result in the service registry). The
// endpoints themselves are annotated next to their handlers in src/http_server.go,
// and src/contract_test.go keeps the two in step: annotations are the contract's
// single source of truth (docs/http-api.md).
//
// Nothing at runtime depends on this: the runtime imports no swaggo package. @version
// is only a fallback for a local `swag init` — the registration step passes the
// release's APP_VERSION, so the registry always sees the deployed hash.
//
// @title        autonomy
// @version      1.0.0
// @description  自主 agent runtime 的 HTTP API：受理指令、把一句话广播给一批 agent（某个 project 下的 / 所有 project 下的）、查任务详情（状态 / 计划 / 所属 project 与组织）、查 agent 工作状态、轮询对话流；以及数据 API —— 把 reason turn 日志当数据读（列表 / 详情 / facets / task 选择器 / 自述），供评测侧调用而不再直接读库；以及优雅重启的对接端点（部署平台重启前先通知、再轮询是否可以重启，服务在这一侧不启动新 run、等在途 run 跑完）。
// @BasePath     /
// @schemes      http
// @host         127.0.0.1:4300
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	autonomy "github.com/kaulie/autonomy/src"
	// Link the database module so its engines (sqlite, postgres) register
	// themselves with the root package's engine registry, which is what
	// autonomy.BootstrapAutonomy -> OpenDefaultStore opens through.
	_ "github.com/kaulie/autonomy/src/db"
)

// version is stamped by build.sh (-X main.version=$APP_VERSION).
var version = "dev"

// defaultAddr is where the runtime listens when AUTONOMY_HTTP_ADDR is not set:
// the port the service contract declares for autonomy. A deployment always sets
// it — scripts/start.sh takes it from SERVICE_PORT and falls back to the same
// number — so this is the port `go run ./cmd/autonomyd` and `go run
// ./cmd/autonomy` agree on.
const defaultAddr = ":4300"

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
	server := autonomy.NewHTTPServer(runtime)

	// SIGTERM is how scripts/stop.sh — and the deployment platform's restart — ends
	// this process (TERM → wait → KILL). Taking it is the process side of the graceful
	// restart the drain endpoints coordinate (src/graceful.go): stop starting new runs,
	// let the runs in flight come back within the grace, then close the store and the
	// provider sessions instead of being killed with a half-written row.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, addr) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	stop() // a second signal is the operator insisting: let it kill us.

	grace := autonomy.ShutdownGrace()
	fmt.Fprintf(os.Stderr, "[autonomy] 优雅停止：不再启动新 run，在途 run 最多等 %s\n", grace)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace+httpServerGrace)
	defer cancel()
	runtime.Shutdown(shutdownCtx)
	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] http shutdown: %v\n", err)
	}
	select {
	case err := <-served:
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	default:
	}
	return nil
}

// httpServerGrace is the extra budget the HTTP server gets on top of the runtime's own
// shutdown grace: a request in flight is a short read or a queue write, so this only
// covers a caller that stalled, and it must not eat into the runs' grace.
const httpServerGrace = 5 * time.Second
