# Claude harness verification — 2026-09-25

Workspace checkout: `agent-10037/autonomy`, branch `feat/agent-10037-claude-harness`.
Base revision: `36e27aa4573f88c2ed3ffce25be1f83425ffdcd9`.

Commands run from the checkout (local `.verification/` artifacts are not committed):

| Command | Result | Local artifact |
| --- | --- | --- |
| `AUTONOMY_LLM_BACKEND=cursor GOCACHE="$PWD/.verification/go-cache" go test ./...` | PASS on final implementation | `.verification/all-tests-delivery.log` |
| `GOCACHE="$PWD/.verification/go-cache" go test -race ./src/llmbackend/claude -v` | PASS | `.verification/claude-race.log` |
| `AUTONOMY_LLM_BACKEND=cursor GOCACHE="$PWD/.verification/go-cache" go test ./src -run 'TestClaudeAccountRunsPlannerAndDelegatedWorker\|TestTheCatalogueEndpointsAnswerOverHTTP' -v` | PASS | `.verification/runtime-claude.log` |
| `GOCACHE="$PWD/.verification/go-cache" go build -o .verification/autonomyd ./cmd/autonomyd` | Exit 0, binary generated | `.verification/autonomyd` |
| `bash -n scripts/start.sh` | Exit 0 | Shell syntax only; did not start/deploy runtime |
| `git diff --check` | Exit 0 | No whitespace errors |
| `claude --version`; `claude --help` | 2.1.31; all used CLI flags present | `.verification/claude-help.txt` |

The runtime integration test creates a Claude account, resolves the planner through the
normal account/session path, runs a turn, acquires a delegated worker, confirms account
inheritance, and runs its turn. A fake executable supplies the stream; this verifies actual
subprocess and runtime plumbing without spending model credentials. Harness tests cover
restart resume, ephemeral sessions, every content block, tool correlation, token/cache/cost
mapping, probes, malformed/error/missing results, exit failure and cancellation.

Earlier attempts exposed and fixed a macOS physical-path assertion and an empty model-list
HTTP assertion. The first broad run inherited the host's `cline` default and failed two
Cursor-specific tests; explicitly choosing `cursor` resolved those. One intermediate run
also hit a temporary-directory cleanup race in `TestATaskCanNameTheAccountItRunsOn`; the
subsequent complete runs passed. Earlier logs remain in `.verification/` for inspection.
The default Go build cache was not writable, so the cache was moved into this workspace.
The successful build emitted a warning that it could not write an external module metadata
cache entry; it still exited 0 and generated the binary.

No authenticated real-model turn or real Claude tool execution was performed. No deployment
was attempted. Permission modes are not an OS sandbox; missing Claude installation or login
must be resolved on the runtime host. PR merge is left to a reviewer because the task policy
explicitly forbids merging one's own PR.
