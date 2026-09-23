/**
 * Codex bridge configuration helpers.
 *
 * The Codex SDK wraps the `codex` CLI (it spawns it and exchanges JSONL over stdio), so a
 * "session" here is a Codex *thread*: `codex.startThread()` opens one,
 * `codex.resumeThread(id)` re-attaches the one an earlier process recorded — threads are
 * persisted in ~/.codex/sessions, which is what makes a restart continuable.
 *
 * Credentials are the CLI's own: `codex auth` stores them in ~/.codex/auth.json, and the
 * SDK injects the key it was configured with (AUTONOMY_CODEX_API_KEY / CODEX_API_KEY) on
 * top. Nothing here invents a key.
 *
 * Reasoning mode maps onto Codex's sandbox: a planner's cycle decides (read-only) while a
 * worker's turn may change the workspace — the same split Cline expresses as plan vs act.
 */

export const PROTOCOL = "codex-bridge/1";

/** The model the CLI uses when the caller names none (the CLI's own default). */
export const DEFAULT_MODEL = "";

export const DEFAULT_SYSTEM_PROMPT =
	"You are an autonomous coding agent working inside the configured workspace. " +
	"Use the available tools to complete the task, verifying your work, and finish " +
	"with a concise summary of what you changed and why.";

/** The reasoning modes the runtime asks for. */
export const MODE_PLAN = "plan";
export const MODE_AGENT = "agent";

/**
 * sandboxModeFor maps a runtime reasoning mode onto the Codex sandbox. Plan work stays
 * read-only; everything else may write the workspace (never `danger-full-access`: the
 * agent's own workspace is where its work belongs).
 */
export function sandboxModeFor(mode) {
	return String(mode || "").trim().toLowerCase() === MODE_PLAN ? "read-only" : "workspace-write";
}

/** coerceText flattens whatever the runtime sent into the prompt text. */
export function coerceText(value) {
	if (typeof value === "string") return value;
	if (value == null) return "";
	if (Array.isArray(value)) return value.map(coerceText).join("\n");
	if (typeof value === "object") return coerceText(value.text ?? value.prompt ?? value.message ?? "");
	return String(value);
}

/**
 * resolveCodexDefaults is what this bridge runs on when the caller configures nothing:
 * the environment (AUTONOMY_CODEX_MODEL / _API_KEY / _BASE_URL), and for the model the
 * CLI's own default (empty means "let the CLI decide"). The CLI's auth file is *not* read
 * here — the CLI and the SDK do that themselves.
 */
export function resolveCodexDefaults(env = process.env) {
	const trim = (v) => (typeof v === "string" ? v.trim() : "");
	return {
		model: trim(env.AUTONOMY_CODEX_MODEL) || DEFAULT_MODEL,
		apiKey: trim(env.AUTONOMY_CODEX_API_KEY) || trim(env.CODEX_API_KEY),
		baseUrl: trim(env.AUTONOMY_CODEX_BASE_URL),
	};
}

/** statusOf maps a Codex turn/thread failure onto the runtime's run status. */
export function statusOf(state) {
	switch (String(state || "").toLowerCase()) {
		case "error":
		case "failed":
			return "error";
		case "aborted":
		case "cancelled":
		case "canceled":
			return "cancelled";
		default:
			return "finished";
	}
}

/** itemText is the text of one completed Codex item, "" for the ones that carry none. */
export function itemText(item) {
	if (!item || typeof item !== "object") return "";
	if (typeof item.text === "string") return item.text;
	if (item.type === "agent_message" && typeof item.message === "string") return item.message;
	return "";
}

/** normalizeUsage shapes a Codex usage object into the bridge's own fields. */
export function normalizeUsage(usage) {
	if (!usage || typeof usage !== "object") return null;
	const num = (v) => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
	const input = num(usage.input_tokens) ?? num(usage.inputTokens);
	const output = num(usage.output_tokens) ?? num(usage.outputTokens);
	const cached = num(usage.cached_input_tokens) ?? num(usage.cachedInputTokens);
	const total = num(usage.total_tokens) ?? num(usage.totalTokens);
	if (input === undefined && output === undefined && cached === undefined && total === undefined) return null;
	return {
		input_tokens: input ?? 0,
		output_tokens: output ?? 0,
		...(cached !== undefined ? { cached_input_tokens: cached } : {}),
		...(total !== undefined ? { total_tokens: total } : {}),
	};
}
