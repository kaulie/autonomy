# Autonomy

A goal-driven agent runtime for autonomous action, world-state awareness, capability composition, and self-verification.

Autonomy is an experimental Agent Runtime built from first principles.

The goal is simple:

Don’t define how the agent should work. Define what needs to be achieved, what the world looks like, and what “done” means. Let the agent figure out the path.

## Concepts

Architecture comes first. Each core concept is documented separately under [`docs/`](docs/README.md):

Context · Task · Agent · Capability · Completion Contract · Delegation · Event · Verification · Policy · …

Start here: **[docs/README.md](docs/README.md)**

## Why

Most agent systems today are still heavily dependent on predefined workflows:

Task → Workflow → Skill A → Skill B → Skill C

This works well for predictable automation, but it limits true autonomy. When the result of one step is uncertain, humans often have to step in, verify it, and decide what happens next.

Autonomy takes a different approach:

Task → Agent → Capability → Action → World State Change → Verification → Done / Re-plan

The workflow is not the primary artifact. It emerges from the agent’s decisions while interacting with the world.

## Core Model

* **Task** — what needs to be achieved.
* **Domain** — the semantic space that defines relevant capabilities.
* **Context** — the environment in which the task exists.
* **Asset** — something in the world that can be observed, referenced, or changed.
* **Capability** — something the system can do to or with an asset.
* **Action** — an actual execution of a capability.
* **Event** — an observable fact or state change.
* **Verification** — determines whether the resulting world state satisfies the goal.
* **Agent** — reasons about the task and chooses what to do next.
* **Runtime** — reliably executes actions and manages their lifecycle.
* **Provider** — a concrete implementation of a capability.

The central relationship is:

Task → Target Asset + Desired State → Agent → Capability → Action → World → Observation / Event → Verification → Completion Contract

## Verification First

A capability may report that its execution succeeded, but that does not necessarily mean the world reached the desired state.

For example: `printer.print(image)` — a successful API call does not prove that the physical image was actually printed correctly.

The system therefore separates:

Capability → Effect + Evidence → External Verification → Completion Contract

Verification can be provided by rules, tests, sensors, models, other agents, or humans.

## Design Principles

* Goals over workflows
* World state over assumptions
* Capabilities over hard-coded procedures
* External verification over self-declared success
* Dynamic planning over predefined execution paths
* Small primitives with recursive composition
* Reliable runtime, flexible agents

Full principle set: [`docs/principles.md`](docs/principles.md)

## Status

Early-stage experimental project.

**Current focus:** flat Go core under `src/` aligned with the architecture baseline — entities vs behavior interfaces — plus a runnable hello loop.

Entity objects: `Task`, `Agent`, `Asset`, `Action`, `Event`.  
Behavior interfaces: `DecisionMaker` (on Agent), `Capability`, `Runtime`, `Verifier`, `World`.

```bash
go test ./...
go run ./cmd/hello
go run ./cmd/autonomyd                    # the runtime: owns the store, the agents, the world; serves the task API
go run ./cmd/autonomy -description "…"    # a client of it: one instruction over HTTP
go run ./cmd/autonomy -broadcast all -description "…"   # or one message to every project's agents
```

The API's contract is not a hand-written spec: every route is **annotated where it is handled**
(`cmd/autonomyd/main.go` for the General API Info, `src/http_server.go` per handler), swag turns the
annotations into OpenAPI, and `scripts/register-contract.sh` registers it — `build.sh` does that at the
end of every release, idempotently. `src/contract_test.go` keeps the annotations and the served routes
one and the same. Annotations cost the runtime nothing (no swaggo import). See [docs/http-api.md](docs/http-api.md).

### Cursor SDK Bridge (LLMReasoner)

Go adapter: `src/cursorsdk` (BridgeManager + Connect client + Agent/Run), generated from `proto/sdk/v1` per [Agent: start here](https://github.com/cursor/sdk-bridge#agent-start-here).

The bridge is a downloaded binary (`third_party/bin/cursor-sdk-bridge`, gitignored). `build.sh` ships it in the release package as `bin/cursor-sdk-bridge` and `scripts/start.sh` points `CURSOR_SDK_BRIDGE_BIN` at it — a deployed runtime's cwd is the runtime dir, where the runtime's own `third_party/…` lookup finds nothing. A build whose checkout has no copy **fetches the pinned version** (`scripts/fetch-bridge.sh`, cached per version on the build machine), so a release package is self-contained for either backend; the packaged copy wins over a path in `backend/.env`, the same rule the port and the Cline bridge follow.

```bash
# regenerate stubs after proto bumps
buf generate --template src/cursorsdk/buf.gen.yaml

export CURSOR_API_KEY=...
./scripts/fetch-bridge.sh          # or set CURSOR_SDK_BRIDGE_BIN
export PROJECT_ROOT=/absolute/path/to/autonomy   # required; policy + SQLite under this root
export AUTONOMY_REASONER=llm
export AUTONOMY_LLM_MODEL=composer-2
# Optional: AUTONOMY_LLM_TIMEOUT=3m (default). Idle budget per provider run: the
# watchdog restarts on every provider event, so a run is aborted only after this
# much silence (send/wait share it) — long runs are never cut by wall clock. A run
# that receives nothing but keepalives still counts as idle.
# Optional: CURSOR_SDK_CALL_TIMEOUT=1m (default). Budget for one *call* to the
# bridge — create/resume/close/delete a session, list or cancel a run. A call is not
# a run: opening a session should take seconds, so a bridge that has not answered
# one by then is wedged, and the call is given up on with "no answer from the bridge
# within 1m0s" instead of holding its run until AUTONOMY_LLM_TIMEOUT fires. Streams
# are untouched by it. 0 (or a negative value) disables the bound.
# The bridge is given none of this process's proxy variables (HTTP_PROXY/HTTPS_PROXY/
# ALL_PROXY/NO_PROXY): its egress is the Cursor API, and a proxy it was never
# configured for is how CreateAgent comes to hang with no error at all. A deployment
# whose Cursor egress really does need one names it for the bridge alone:
#   CURSOR_SDK_BRIDGE_PROXY=http://127.0.0.1:7897
# The Go client already disables proxy for loopback bridge RPCs.
# Stage timing logs go to stderr by default. AUTONOMY_LLM_DEBUG=0 silences SDK traces;
# AUTONOMY_LLM_DEBUG=1 also dumps prompt body and assistant chunk sizes.
# The raw llm_events stream (one row per provider event) is off by default: it is a
# leaf table serving replay, so a run stores its reason_turns header and its
# llm_messages conversation instead. AUTONOMY_LLM_EVENTS=1 stores every event too.
# Optional: AUTONOMY_STORE_ENGINE=sqlite (default) picks the storage engine;
# AUTONOMY_STORE_DSN overrides its default DSN (sqlite: ~/database/autonomy/autonomy.db,
# or $AUTONOMY_DATA_DIR/autonomy.db — one database for the whole machine).
# The other built-in engine is postgres: AUTONOMY_STORE_ENGINE=postgres with a DSN from
# AUTONOMY_STORE_DSN or AUTONOMY_POSTGRES_DSN. It is a database of its own — the sqlite
# file keeps its data, nothing is migrated — and it creates its schema on open
# (see docs/store.md).
go run ./cmd/autonomyd
```

`LLMReasoner` reads `$PROJECT_ROOT/src/agent_policy/AGENT_V2.md` at runtime, passes it through to the model (replacing `{{CONSTRUCTS}}` from bootstrap-registered capabilities in `src/capability/`, plus any future `{{...}}` placeholders). Each capability arrives with what it declares about itself — `name` / `domain` / `provider` / `description`, and its `input` / `output` fields (`spec.Declared`, `src/capability/spec`) — so a plan step can be written from that list instead of from prose. `{{CONSTRAINTS}}` is the runtime's own facts (the task, the sandbox) plus its policy file (`src/agent_policy/CONSTRAINTS.json`, see `src/policy.go` and [docs/policy.md](docs/policy.md)): a rule about a domain is a sentence someone owns in a file, not a line in the prompt renderer. `{{AGENT}}` is the agent's own identity as its own `## Agent` section — role (`planner` / `worker`), the purpose a worker was acquired for, id, name, backend, model, lifecycle, workspace — and it is the frame's, not the delta's, because an agent does not become someone else between cycles (see [docs/agent.md](docs/agent.md)). Because the reasoning session is multi-turn, that **frame** is sent only on a session's *first* decision cycle; every later cycle sends just the per-cycle **delta** — fenced JSON with the current Task / Context Entity / World / Runtime Context (the latter carrying `previous_actions`, so a re-plan is not blind), plus the cycle number and "answer with the known Output Schema". A new session (Cursor `Create`, a new Cline handle, a mode/cwd change, a bridge restart) sends the frame again, and only a **successful** run marks it delivered (`Agent.needsLLMFrame()` / `markLLMFrameSent()`, see [docs/execution-loop.md](docs/execution-loop.md) and `src/llm_frame_test.go`). The delegated worker's prompt is a repository file too — `$PROJECT_ROOT/src/agent_policy/CODE_EDIT.md` (`{{WORKSPACE}}` / `{{GOAL}}` plus the delegating runtime's frame: `{{WORLD}}` / `{{RUNTIME_CONTEXT}}` / `{{COMPLETION_PRINCIPLES}}` / `{{CONSTRAINTS}}` / `{{CONSTRUCTS}}`), read and rendered on every delegation — so prompt wording never lives in Go source. Every capability that needs an agent gets that frame the same way (`src/capability/broker`: `WorkerFrame` / `RenderWorkerPrompt`); `deployment.monitor` delegates to a monitoring agent through `$PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md` (its own coordinates plus the same frame; see [docs/deployment-monitor.md](docs/deployment-monitor.md)). Bootstrap registers agents only via `AgentFactory`; Cursor is one backend (`AttachCursor` / `newCursorClient` once). The delta’s **Context Entity** is not the reference string a task states: before each cycle’s prompt is rendered, the runtime resolves the task’s `context_ref` through the **context builder** (an independent module, `src/context_builder`, [docs/context-builder.md](docs/context-builder.md)) — this process’s registered context containers plus the platform’s registries (the project registry, the organization catalogue naming the department the project belongs to, the service registry naming the services that department has — each with the repository its code lives in — and the task registry, which turns a task id into the task and the project it is in). A registry that is down means a thinner prompt, never a failed cycle; `AUTONOMY_CONTEXT_BUILDER=0` switches it off. Every agent has an **inbox** (`src/inbox.go`, [docs/inbox.md](docs/inbox.md)): the messages addressed to it — from the user (an instruction), from another agent (a capability's delegated prompt) or from the runtime (a stop) — processed one at a time, in the order they arrived, and persisted so a restart carries on — a message a previous process left `running` is picked up at boot by that agent itself (the runtime heals a cut run instead of waiting for the next instruction; [docs/graceful-restart.md](docs/graceful-restart.md)). Instructions are therefore accepted continuously: one arriving while the agent is busy is queued behind what it is doing. An agent is **kept, and resident, by default**: a run ending does not stop it (its provider session stays, so the next instruction continues the same conversation) and does not delete it, and a task's row names the agent it is paired with — so an instruction arriving after a restart finds that agent by `tasks.agent_id`, rebuilds its handle and hands it back to the factory instead of creating a second one on the same task (`resumeAgentForTask`, see [docs/agent.md](docs/agent.md)). Accepting an instruction opens no provider session — it queues the message and answers, and the session is opened by the turn that needs it — which is what keeps `POST /api/tasks`, and every accept a broadcast fans out, off the bridge. Every agent's conversation with its LLM is **one session** (`src/llm_session.go`, `LLMSession`): the task's own agent and a delegated worker take their turns through the same code, and the agent's role is what makes a turn a plan turn or a delegated turn — one watchdog, one truncated-turn retry, one place where a run is recorded (see [docs/session.md](docs/session.md)). `code_edit` acquires a Cursor-backed agent through `Runtime.AcquireAgent` (not a private Cursor client). `service.deploy` triggers a service's deployment pipeline at a branch through the deployment control plane (`POST /api/deploy-notify {serviceId, ref}` at `DEPLOYMENT_API_URL`, default `http://127.0.0.1:4220`) and returns the pipeline id without waiting for packaging/deploying; every call carries the caller identity the control plane now requires (`identity_role` / `identity_id` headers, phase-1 身份校验), resolved input → `IDENTITY_ROLE` / `IDENTITY_ID` → `agent:autonomy`, so a deploy is always attributable in the panel and the audit trail. Storage is pluggable behind a unified `Store` contract — seven ports (tasks / agents / inbox / conversation / execution / verification / turn log) whose union is what a database engine implements, with each component depending only on the port it uses; the default `sqlite` engine lives at `~/database/autonomy/autonomy.db` (`$AUTONOMY_DATA_DIR`, documented in [docs/store.md](docs/store.md)) — one database for the whole machine, so the deployed runtime, a dev run and the benchmark tool all read the same file. A second engine, `postgres` (`AUTONOMY_STORE_ENGINE=postgres`, same ports, its own schema and SQL), keeps its own database: the sqlite file keeps its data and nothing is migrated between them. Each Agent gets `AGENT_WORKSPACE=/Users/gaolei/agent-workspace-sandbox/{agent_name}/`. Decisions map registered capabilities to `CapabilityAction` → `Capability.Run`.

Every LLM interaction is persisted as a `reason_turns` run header and the user input and assistant output as two linked `llm_messages` rows (`parent_id` traces a return back to its specific input); the raw provider stream in `llm_events` (`turn_id` → header, ordered by `seq`, verbatim `payload`) is opt-in, because `llm_events` is a **leaf**: it points at the header and nothing points back at it (the
schema declares no foreign keys at all), so dropping or rebuilding the stream touches no other table — and leaving it off by default (`AUTONOMY_LLM_EVENTS=1` turns it back on) is that same fact, exercised on every run. New providers plug in by mapping their native stream into the neutral `LLMEvent` shape via `LLMStreamAdapter` — `cursor` (Cursor SDK bridge) and `cline` (Cline SDK bridge) are
implemented today — see [docs/llm-message.md](docs/llm-message.md), [docs/llm-event-stream.md](docs/llm-event-stream.md)
and [docs/cline-reasoner.md](docs/cline-reasoner.md).

Live smoke (optional): `CURSOR_LIVE=1 go test ./src -run TestLLMReasonerLive -timeout 5m -v`

### Cline backend (resident agent sessions)

`AUTONOMY_LLM_BACKEND=cline` runs acquired coding agents on the Cline SDK instead
of the Cursor SDK bridge. Cline's agent core is TypeScript-only, so Go drives it
through a small Node bridge (`src/clinesdk/bridge/bridge.mjs`, NDJSON over stdio)
that owns the resident Cline session; the Go side owns process lifecycle, request
correlation, event fan-out and the mapping onto neutral `LLMEvent`s. Design,
protocol and mapping: [docs/cline-reasoner.md](docs/cline-reasoner.md).

The backend is the **runtime's** (`autonomyd`) setting, not the CLI's: `cmd/autonomy`
is an HTTP client, so exporting the variable around `go run ./cmd/autonomy` changes
nothing — that run's failure is what the runtime decided. A running runtime says
which backend it is on: `GET /health` → `{"status":"ok","llm_backend":"cursor","llm_model":"composer-2"}`.
How to switch it (dev and deployed) — and the one boundary that matters on a deployed
runtime, where the release package now **ships the Cline bridge** (its sources plus a
packed production install of `@cline/sdk`, extracted by `scripts/start.sh` on start, so
every deploy refreshes it) — plus how to read the `empty model response … You're out of
usage` failure that motivates a switch: [docs/llm-backend.md](docs/llm-backend.md).

```bash
./scripts/install-cline-bridge.sh            # npm install @cline/sdk for the bridge
export AUTONOMY_LLM_BACKEND=cline            # enough when the machine ran `cline auth`
# Optional explicit provider/model (otherwise the saved cline auth config is used;
# AUTONOMY_LLM_MODEL is the default Cursor backend's model and does not apply here):
export AUTONOMY_CLINE_PROVIDER=deepseek
export AUTONOMY_CLINE_MODEL=deepseek-v4-pro
export AUTONOMY_CLINE_API_KEY=sk-...         # optional: `cline auth` credentials are reused
export PROJECT_ROOT=$(pwd)
export AUTONOMY_REASONER=llm
go run ./cmd/autonomyd                     # the runtime; `go run ./cmd/autonomy` is its HTTP client
```

Live smoke (optional): `CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=... AUTONOMY_CLINE_MODEL=... go test ./src/clinesdk -run TestClineBridgeLive -timeout 6m -v`

The run log is the conversation, not the token stream: autonomy logs one line per
`llm_messages` row as it is written — `[autonomy] llm seq=2 tool execute_command
call=… args=… -> …` — so thinking and tool activity is visible while a long run is
still streaming. `AUTONOMY_LLM_TRACE=0` silences it, `AUTONOMY_LLM_TRACE_MAX` caps
each field. The Cline bridge stays quiet by default; `AUTONOMY_CLINE_TRACE=signal`
adds its own milestone lines and `=1` the full per-event firehose for debugging.

Bridge unit tests (trace policy, config resolution): `cd src/clinesdk/bridge && npm test`.

### Graceful restart

The deployment platform restarts a service with `stop` + `start`. A service that has
registered **two endpoints** in its deployment config is restarted gracefully instead:
the platform announces the restart, then polls until the service says a restart is safe.

Autonomy serves both (`src/graceful.go`):

```bash
curl -sS -X POST http://127.0.0.1:4300/api/ops/restart-notify \
  -H 'content-type: application/json' \
  -d '{"serviceId":"autonomy","requestId":"deploy-1","deployment":"deployment-abc12345","message":"restarting"}'
curl -sS http://127.0.0.1:4300/api/ops/restart-status   # {"canRestart":…,"running":…,"held":…,"reason":"…"}
```

Register exactly those two URLs (notify + status) as autonomy's graceful endpoints in the
deployment panel: `http://127.0.0.1:4300/api/ops/restart-notify` and
`http://127.0.0.1:4300/api/ops/restart-status` — with both configured, a deploy waits for
the runs in flight instead of cutting them; with neither, it restarts the hard way.

What "safe" means: no run is in flight. While draining, an instruction is still **accepted**
(it is a row in its agent's inbox) but not **started** — the process that comes up after the
restart starts it, so nothing a caller handed over is lost. A drain that never gets its
restart ends by itself after `AUTONOMY_DRAIN_TIMEOUT` (10 minutes, `0` = never) so a failed
deploy cannot wedge the service. `SIGTERM` (what `scripts/stop.sh` sends) is handled the same
way: no new run, the runs in flight get `AUTONOMY_SHUTDOWN_GRACE` (10s) to come back, then
they are stopped and the store and provider sessions are closed properly. See
[docs/graceful-restart.md](docs/graceful-restart.md).

The hello demo health-checks a fake service and finishes only when `StateVerifier` sees `Contract.ExpectedState` on the world — capability success alone is not enough.

Decision cycle: `BuildDecisionContext` → `Decide` → `Execute` → `Record` → `UpdateWorld` → `Verify` / `ShouldTerminate`.

Built with Go (Golang).
