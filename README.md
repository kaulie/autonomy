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
go run ./cmd/autonomy
```

### Cursor SDK Bridge (LLMReasoner)

Go adapter: `src/cursorsdk` (BridgeManager + Connect client + Agent/Run), generated from `proto/sdk/v1` per [Agent: start here](https://github.com/cursor/sdk-bridge#agent-start-here).

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
# much silence (create/send/wait share it) — long runs are never cut by wall
# clock. A run that receives nothing but keepalives still counts as idle.
# Keep HTTPS_PROXY if you need it for Cursor API egress. The Go client already
# disables proxy for loopback Bridge RPCs; unsetting proxy can make CreateAgent hang.
# Stage timing logs go to stderr by default. AUTONOMY_LLM_DEBUG=0 silences SDK traces;
# AUTONOMY_LLM_DEBUG=1 also dumps prompt body and assistant chunk sizes.
# Optional: AUTONOMY_LLM_EVENTS=0 stores only the reason_turns run header and skips
# the raw llm_events stream (default: store every provider event).
# Optional: AUTONOMY_STORE_ENGINE=sqlite (default) picks the storage engine;
# AUTONOMY_STORE_DSN overrides its default DSN (sqlite: $PROJECT_ROOT/data/autonomy.db).
go run ./cmd/autonomy
```

`LLMReasoner` reads `$PROJECT_ROOT/src/agent_policy/AGENT_V2.md` at runtime, passes it through to the model (replacing `{{CONSTRUCTS}}` from bootstrap-registered capabilities in `src/capability/`, plus any future `{{...}}` placeholders). Each capability arrives with what it declares about itself — `name` / `domain` / `provider` / `description`, and its `input` / `output` fields (`spec.Declared`, `src/capability/spec`) — so a plan step can be written from that list instead of from prose. `{{CONSTRAINTS}}` is the runtime's own facts (the task, the sandbox) plus its policy file (`src/agent_policy/CONSTRAINTS.json`, see `src/policy.go` and [docs/policy.md](docs/policy.md)): a rule about a domain is a sentence someone owns in a file, not a line in the prompt renderer. `{{AGENT}}` is the agent's own identity as its own `## Agent` section — role (`planner` / `worker`), the purpose a worker was acquired for, id, name, backend, model, lifecycle, workspace — and it is the frame's, not the delta's, because an agent does not become someone else between cycles (see [docs/agent.md](docs/agent.md)). Because the reasoning session is multi-turn, that **frame** is sent only on a session's *first* decision cycle; every later cycle sends just the per-cycle **delta** — fenced JSON with the current Task / Context Entity / World / Runtime Context (the latter carrying `previous_actions`, so a re-plan is not blind), plus the cycle number and "answer with the known Output Schema". A new session (Cursor `Create`, a new Cline handle, a mode/cwd change, a bridge restart) sends the frame again, and only a **successful** run marks it delivered (`Agent.needsLLMFrame()` / `markLLMFrameSent()`, see [docs/execution-loop.md](docs/execution-loop.md) and `src/llm_frame_test.go`). The delegated worker's prompt is a repository file too — `$PROJECT_ROOT/src/agent_policy/CODE_EDIT.md` (`{{WORKSPACE}}` / `{{GOAL}}` plus the delegating runtime's frame: `{{WORLD}}` / `{{RUNTIME_CONTEXT}}` / `{{COMPLETION_PRINCIPLES}}` / `{{CONSTRAINTS}}` / `{{CONSTRUCTS}}`), read and rendered on every delegation — so prompt wording never lives in Go source. Every capability that needs an agent gets that frame the same way (`src/capability/broker`: `WorkerFrame` / `RenderWorkerPrompt`); `deployment.monitor` delegates to a monitoring agent through `$PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md` (its own coordinates plus the same frame; see [docs/deployment-monitor.md](docs/deployment-monitor.md)). Bootstrap registers agents only via `AgentFactory`; Cursor is one backend (`AttachCursor` / `newCursorClient` once). `code_edit` acquires a Cursor-backed agent through `Runtime.AcquireAgent` (not a private Cursor client). `service.deploy` triggers a service's deployment pipeline at a branch through the deployment control plane (`POST /api/deploy-notify {serviceId, ref}` at `DEPLOYMENT_API_URL`, default `http://127.0.0.1:4220`) and returns the pipeline id without waiting for packaging/deploying. Storage is pluggable behind a unified `Store` interface; the default `sqlite` engine lives at `$PROJECT_ROOT/data/autonomy.db` (see [docs/store.md](docs/store.md)). Each Agent gets `AGENT_WORKSPACE=/Users/gaolei/agent-workspace-sandbox/{agent_name}/`. Decisions map registered capabilities to `CapabilityAction` → `Capability.Run`.

Every LLM interaction is persisted as a `reason_turns` run header, the user input and assistant output as two linked `llm_messages` rows (`parent_id` traces a return back to its specific input), plus the raw provider stream in `llm_events` (`turn_id` → header, ordered by `seq`, verbatim `payload`). New providers plug in by mapping their native stream into the neutral `LLMEvent` shape via `LLMStreamAdapter` — `cursor` (Cursor SDK bridge) and `cline` (Cline SDK bridge) are
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
go run ./cmd/autonomy
```

Live smoke (optional): `CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=... AUTONOMY_CLINE_MODEL=... go test ./src/clinesdk -run TestClineBridgeLive -timeout 6m -v`

The run log is the conversation, not the token stream: autonomy logs one line per
`llm_messages` row as it is written — `[autonomy] llm seq=2 tool execute_command
call=… args=… -> …` — so thinking and tool activity is visible while a long run is
still streaming. `AUTONOMY_LLM_TRACE=0` silences it, `AUTONOMY_LLM_TRACE_MAX` caps
each field. The Cline bridge stays quiet by default; `AUTONOMY_CLINE_TRACE=signal`
adds its own milestone lines and `=1` the full per-event firehose for debugging.

Bridge unit tests (trace policy, config resolution): `cd src/clinesdk/bridge && npm test`.


The hello demo health-checks a fake service and finishes only when `StateVerifier` sees `Contract.ExpectedState` on the world — capability success alone is not enough.

Decision cycle: `BuildDecisionContext` → `Decide` → `Execute` → `Record` → `UpdateWorld` → `Verify` / `ShouldTerminate`.

Built with Go (Golang).
