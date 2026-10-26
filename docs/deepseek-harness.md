# DeepSeek harness（原生）

The `deepseek` harness implements `llmbackend.SessionImpl` in pure Go, directly over
DeepSeek's OpenAI-compatible Chat Completions API (`POST /chat/completions`, streamed, with
tool calling). Unlike the other four harnesses there is no SDK, CLI or Node bridge behind it,
so the harness owns what they get from theirs: the conversation, the tools, and the agent
loop. Importing `src/llmbackend/all` registers it for planners, acquired workers, account
catalogues and account probes. It adds no Go dependency (`net/http` + `encoding/json`).

## Setup

1. Add an enabled account at `/accounts`: harness `deepseek`, vendor `deepseek`, the API key,
   optional base URL (blank = `https://api.deepseek.com`; any OpenAI-compatible gateway works)
   and optional model (blank = `deepseek-v4-pro`), plus the agent workspace root.
2. Select that account for the task, or set `AUTONOMY_LLM_BACKEND=deepseek` on the runtime
   and make it the default `deepseek` account.
3. Verify it at `/accounts`: the probe calls `GET /models` with the key (the credential really
   is checked); a live probe runs one short turn and spends tokens.

Credentials and model come from the account only (`Facts.Creds`); the harness reads no
`DEEPSEEK_*` variable. A task's `model` outranks the account's, which outranks the default.
`scripts/start.sh` has no bridge to check for this backend and says so.

| Variable | Default | Meaning |
|---|---|---|
| `AUTONOMY_DEEPSEEK_MAX_ITERATIONS` | `50` | model requests one turn's tool loop may make before it fails |
| `AUTONOMY_DEEPSEEK_SESSIONS_DIR` | `~/.autonomy/deepseek/sessions` | where conversations are stored |

## One turn

`Prompt` appends the user message to the session's history and loops:

1. stream one completion with the history and the tool definitions;
2. no tool call → that text is the answer, the turn ends;
3. otherwise run every tool call in the agent's workspace, append each result as a `tool`
   message, and go to 1 — at most `AUTONOMY_DEEPSEEK_MAX_ITERATIONS` requests.

A failed turn (HTTP error, cancelled context, loop bound) returns the error with
`RunResult.Status=error|cancelled` and leaves the stored history as it was before the turn.
Every stream line calls `llmrun.Touch`, so the runtime's idle watchdog only fires on silence.
The first message of a new conversation is a short harness system prompt (workspace, mode,
tools); the runtime's role prompt arrives as the first turn (`SystemInject` first_turn).

DeepSeek's `reasoning_content` is sent back only for the turn still running (its tool loop
needs it); it is stripped from turns already answered, as DeepSeek's thinking mode asks.

### Tools

| Tool | Mode | What it does |
|---|---|---|
| `read_file` | plan + agent | read a file (optional 1-based `offset` / `limit`) |
| `list_files` | plan + agent | recursive listing, optional name glob (skips `.git`, `node_modules`) |
| `search_files` | plan + agent | RE2 regex over file contents → `path:line: text` |
| `write_file` | agent | create/overwrite a file (parents created) |
| `edit_file` | agent | exact-string replace; must be unique unless `replace_all` |
| `run_shell` | agent | `bash -c` in the workspace, combined output + exit code, timeout 120 s (max 600 s) |

MCP servers (`AUTONOMY_MCP_SERVERS_FILE`) are not mounted by this harness yet: its tool list is
these six. This mirrors Claude's `Read/Glob/Grep` (plan) and `Edit/Write/Bash` (worker). Paths are resolved
against the workspace and a path that leaves it is refused; output is capped at 64 KiB per call.
The shell runs as the runtime user in the workspace directory; it is not a sandbox (symlinks and
the shell can reach outside, the same trust the Claude harness's `Bash` has).

## Sessions and resume

DeepSeek's API is stateless, so each session is a JSON transcript
(`<sessions dir>/ds-<16 hex>.json`) written after every successful turn. As in the claude
harness, conversations are kept per (mode, workspace); a planner's session id is recorded on
the agent row (`SetSessionID`), and a new process attaching with that id loads the transcript
and reports `Resumed()=true`, `ResumedFrom()=<id>`. A missing transcript starts a fresh session
rather than failing. A one-shot worker (`Ephemeral`) is not stored, and `Dispose` deletes it.

## Events

`Prompt` maps every native event through `llmbackend.MapNativeLLMEvent(ProviderDeepseekHarness, …)`
into the sink the runtime passes (`LLMTrace.Emit`; the runtime opens and finishes the trace).

| native `type` | channel / kind |
|---|---|
| `reasoning_delta` / `reasoning_end` | thought / `thought_delta` / `thought_end` |
| `content_delta` | assistant / `assistant_delta` |
| `tool_call` | tool / `tool_call_started` (`call_id`, `name`, `args`, `status=running`) |
| `tool_result` | tool / `tool_call_completed` (`result`, `status=completed|failed`, `duration_ms`) |
| `tool_call_delta` | meta (argument fragments, kept for replay) |
| `usage` | status / `usage` (`prompt_tokens`→`input_tokens`, `prompt_cache_hit_tokens`→`cache_read_tokens` …) |
| `finish` | status / `status` (`finish_reason`) |
| `error` | error / `error` (`message`) |

`RunResult.Usage` sums every completion of the turn; cost is not reported (DeepSeek's stream
carries none).

## Tests

`go test ./src/llmbackend/deepseek/` runs against an `httptest` fake DeepSeek server: a streamed
text reply, a tool-call round trip (fragmented arguments, `write_file` + `run_shell`), event
mapping, registration/selection/probe, resume, workspace confinement, the loop bound and API
errors. `TestLive` runs one real turn when `DEEPSEEK_API_KEY` is set (optional
`DEEPSEEK_BASE_URL`, `DEEPSEEK_MODEL`) and is skipped otherwise.
