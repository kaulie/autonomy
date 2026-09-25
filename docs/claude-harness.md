# Claude Code harness

The `claude` harness implements `llmbackend.SessionImpl` directly over the Claude Code
CLI. Importing `src/llmbackend/all` registers it for planners, acquired workers, account
catalogues and account probes. No Node bridge or additional Go dependency is required.

## Setup

1. Install Claude Code on the runtime host. `claude --version` must work for the runtime user.
   Set `AUTONOMY_CLAUDE_BIN` to an absolute executable path when it is not on PATH.
2. Add an enabled account at `/accounts`: harness `claude`, vendor `anthropic`, optional
   API key, base URL and model, and the desired agent workspace root. With no key, the CLI
   uses its saved login. Models are entered explicitly or left blank for Claude's default;
   the catalogue deliberately returns no hardcoded model list.
3. Select that account for the task, or set `AUTONOMY_LLM_BACKEND=claude` on the runtime
   and make the account the default Claude account. `claude_code` is an accepted alias.

Credentials and model selection come from the account, not runtime environment variables.
The child environment removes `ANTHROPIC_*`, `CLAUDE_CODE_*` and `CLAUDECODE`, then adds
the account's `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL` when present. Claude's own saved
configuration still applies. Configure that login/settings for the runtime user accordingly.
Secrets are not passed in process arguments or included in harness errors.

`scripts/start.sh` checks `--version` before starting a runtime with this backend. It does
not install Claude or authenticate it. A non-live account probe only checks CLI execution;
a live probe runs a short turn in a temporary directory and spends model usage.

## Execution and persistence

Each turn pipes the prompt over stdin to `claude --print --verbose --output-format stream-json`.
The CLI runs in the account-selected agent workspace. Planner turns use permission mode
`plan`. Workers use `acceptEdits` with `Read,Edit,Write,Glob,Grep,Bash` explicitly allowed.
Other tools follow Claude's configured permission policy. These are CLI permissions, **not
an OS filesystem sandbox**; operators requiring isolation must run the runtime in one.
The harness never enables `bypassPermissions`.

Sessions are keyed by mode and workspace. Planner session IDs from the native stream are
persisted on the agent row and passed as `--resume` after restart. Worker sessions do not
overwrite that planner ID. Ephemeral agents use `--no-session-persistence`. Disposing a
resident session forgets local handles but keeps Claude's own saved transcript. An invalid
resume ID fails explicitly; the harness does not silently rerun a prompt on a fresh session.

Assistant, thinking and tool content blocks become neutral events, with tool IDs preserved
for correlation. Original envelopes remain available for replay. Terminal results supply
token/cache usage and USD cost when reported. Provider errors, invalid JSON, process errors,
missing terminal results and empty answers fail the turn. Context cancellation terminates
the CLI and closes the stream; subprocess tool cleanup remains the CLI's responsibility.

Protocol references: [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)
and the installed CLI's `claude --help`. The flags were checked against Claude Code 2.1.31.

## Verification

`go test ./src/llmbackend/claude -v` launches a fake executable through the real subprocess
path. It checks account credentials/environment isolation, stdin, cwd, planner/worker
flags, restart resume, ephemeral persistence, event mapping, token/cost accounting,
non-live/live probes, malformed/missing/error results, nonzero exits and cancellation.
`go test ./src -run TestTheCatalogueEndpointsAnswerOverHTTP` checks Claude's HTTP catalogue.
These tests use no model credentials and do not claim a successful real Claude model turn.
