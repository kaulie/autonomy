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
# Optional: AUTONOMY_LLM_TIMEOUT=3m (default). CreateAgent/Send/Wait share this deadline.
# Keep HTTPS_PROXY if you need it for Cursor API egress. The Go client already
# disables proxy for loopback Bridge RPCs; unsetting proxy can make CreateAgent hang.
# Stage timing logs go to stderr by default. AUTONOMY_LLM_DEBUG=0 silences SDK traces;
# AUTONOMY_LLM_DEBUG=1 also dumps prompt body and assistant chunk sizes.
# Optional: AUTONOMY_LLM_EVENTS=0 stores only the reason_turns run header and skips
# the raw llm_events stream (default: store every provider event).
go run ./cmd/autonomy
```

`LLMReasoner` reads `$PROJECT_ROOT/src/agent_policy/AGENT_V2.md` at runtime, passes it through to the model (replacing `{{CONSTRUCTS}}` from bootstrap-registered capabilities in `src/capability/`, plus any future `{{...}}` placeholders), then appends a structured **Runtime Context** appendix (fenced JSON for Agent / Goal / World / optional Additional Input). Bootstrap registers agents only via `AgentFactory`; Cursor is one backend (`AttachCursor` / `newCursorClient` once). `code_edit` acquires a Cursor-backed agent through `Runtime.AcquireAgent` (not a private Cursor client). SQLite lives at `$PROJECT_ROOT/data/autonomy.db`. Each Agent gets `AGENT_WORKSPACE=/Users/gaolei/agent-workspace-sandbox/{agent_name}/`. Decisions map registered capabilities to `CapabilityAction` → `Capability.Run`.

Every LLM interaction is persisted as a `reason_turns` run header plus its raw provider stream in `llm_events` (`turn_id` → header, ordered by `seq`, verbatim `payload`). New providers plug in by mapping their native stream into the neutral `LLMEvent` shape via `LLMStreamAdapter` — see [docs/llm-event-stream.md](docs/llm-event-stream.md).

Live smoke (optional): `CURSOR_LIVE=1 go test ./src -run TestLLMReasonerLive -timeout 5m -v`


The hello demo health-checks a fake service and finishes only when `StateVerifier` sees `Contract.ExpectedState` on the world — capability success alone is not enough.

Decision cycle: `BuildDecisionContext` → `Decide` → `Execute` → `Record` → `UpdateWorld` → `Verify` / `ShouldTerminate`.

Built with Go (Golang).
