/**
 * Trace policy for the Cline bridge.
 *
 * A Cline run emits thousands of events and most of them are per-token noise:
 * `chunk` is the SDK's own JSON echo of the stream, and text/reasoning arrive as
 * one `content_start` per *delta*. What a human reading a log needs is the run's
 * skeleton — what the model is thinking, which tool it calls with which
 * arguments, and what came back.
 *
 * So the bridge traces at three levels:
 *
 *   signal (default, AUTONOMY_CLINE_TRACE=0)  milestones, with their content
 *   full   (AUTONOMY_CLINE_TRACE=1)           every raw event, one line each
 *   silent (AUTONOMY_LLM_DEBUG=0)             nothing at all
 *
 * AUTONOMY_CLINE_TRACE wins when it is set, so `=0` means "signal only" rather
 * than "silent"; AUTONOMY_LLM_DEBUG keeps its Cursor-client meaning (=1 verbose
 * firehose, =0 silence) so one variable can quiet both backends. Use
 * AUTONOMY_CLINE_TRACE=silent for a quiet Cline bridge.
 */
import { coerceText, messageOf } from "./config.mjs";

/** DEFAULT_WIDTH is the per-field character budget for a signal line. */
export const DEFAULT_WIDTH = 600;

const LEVEL_ON = new Set(["1", "true", "on", "yes", "full", "verbose", "debug", "trace", "all"]);
const LEVEL_OFF = new Set(["0", "false", "off", "no", "silent", "quiet", "none"]);
const LEVEL_QUIET = new Set(["silent", "quiet", "none"]);
/** SKIPPED_CORE lists the core event types that never make a signal line. */
const SKIPPED_CORE = new Set(["chunk", "session_snapshot", "hook"]);

function norm(value) {
	return String(value ?? "").trim().toLowerCase();
}

/**
 * traceLevel resolves the bridge trace level: "full", "signal" or "silent".
 * AUTONOMY_CLINE_TRACE is the specific knob (anything unrecognized means
 * "signal", including 0/false/off); AUTONOMY_LLM_DEBUG is the shared one.
 */
export function traceLevel(env = process.env) {
	const cline = norm(env.AUTONOMY_CLINE_TRACE);
	if (cline) {
		if (LEVEL_QUIET.has(cline)) return "silent";
		return LEVEL_ON.has(cline) ? "full" : "signal";
	}
	const debug = norm(env.AUTONOMY_LLM_DEBUG);
	if (LEVEL_ON.has(debug)) return "full";
	if (LEVEL_OFF.has(debug)) return "silent";
	return "signal";
}

/**
 * traceWidth is the character budget for one condensed field, so a long
 * thinking block or tool output stays on one readable line.
 * AUTONOMY_CLINE_TRACE_MAX overrides it.
 */
export function traceWidth(env = process.env) {
	const raw = Number.parseInt(String(env.AUTONOMY_CLINE_TRACE_MAX ?? "").trim(), 10);
	return Number.isFinite(raw) && raw > 0 ? raw : DEFAULT_WIDTH;
}

/**
 * condense renders one field as a single log line: whitespace (a thinking block
 * is prose with line breaks) collapses to spaces, and anything past the width
 * budget is cut with a marker so a truncated line is obvious.
 */
export function condense(value, width = DEFAULT_WIDTH) {
	if (value == null) return "";
	const text = (typeof value === "string" ? value : coerceText(value)).replace(/\s+/g, " ").trim();
	if (text.length <= width) return text;
	return `${text.slice(0, width)}…(+${text.length - width}ch)`;
}

/** eventLabel names one core event, for the firehose trace lines. */
export function eventLabel(event) {
	const inner = event?.payload?.event ?? event;
	const type = inner?.type ?? event?.type ?? "?";
	const content = inner?.contentType ? `:${inner.contentType}` : "";
	return `${event?.type ?? "?"}/${type}${content}`;
}

/**
 * signalLine renders one event as a milestone line, or null when the event is
 * per-token noise (the `chunk` echo stream, content deltas) that the block line
 * already covers.
 */
export function signalLine(event, width = DEFAULT_WIDTH) {
	const core = String(event?.type ?? "");
	// chunk: the SDK's own echo of the agent stream. session_snapshot: a metadata
	// dump (the bridge already logs the session it started). hook: the tool_call/
	// tool_result/agent_end notifications duplicate the agent events below.
	if (SKIPPED_CORE.has(core)) return null;
	const payload = event?.payload ?? {};
	if (core === "agent_event") {
		return agentSignalLine(payload.event ?? event, width);
	}
	const extra = payload.status ?? payload.reason ?? "";
	return extra === "" ? core : `${core} ${condense(extra, width)}`;
}

/** agentSignalLine classifies one inner agent event (payload.event). */
function agentSignalLine(inner, width) {
	const type = String(inner?.type ?? "");
	const contentType = String(inner?.contentType ?? "");
	switch (type) {
		case "content_start":
			// Text/reasoning deltas (one event per chunk) are the noise we skip:
			// the whole block is printed when it ends.
			return contentType === "tool" ? toolStartLine(inner, width) : null;
		case "content_end":
			switch (contentType) {
				case "reasoning":
				case "thinking":
					if (inner.redacted) return "think <redacted>";
					return textLine("think", inner.reasoning ?? inner.text, width);
				case "text":
					return textLine("assistant", inner.text, width);
				case "tool":
					return toolEndLine(inner, width);
				default:
					return null;
			}
		case "content_update":
			return null; // streaming tool output; the end event carries the result
		case "iteration_start":
			return `iteration ${intOf(inner.iteration)} start`;
		case "iteration_end":
			return `iteration ${intOf(inner.iteration)} end tools=${intOf(inner.toolCallCount)}`;
		case "usage":
			return usageLine(inner);
		case "notice":
			return textLine(`notice ${inner.noticeType ?? "status"}`, inner.message ?? inner.reason, width);
		case "done":
			return `done ${condense(inner.reason ?? "", width)} iterations=${intOf(inner.iterations)}`;
		case "error":
			return `error ${condense(messageOf(inner.error ?? inner.message, "agent error"), width)}`;
		default:
			return type === "" ? null : type;
	}
}

function textLine(label, value, width) {
	const text = condense(value, width);
	return text === "" ? null : `${label} ${text}`;
}

function toolStartLine(inner, width) {
	const args = inner.input === undefined ? "" : ` args=${condense(inner.input, width)}`;
	return `tool ${inner.toolName ?? "tool"} start call=${inner.toolCallId ?? "?"}${args}`;
}

function toolEndLine(inner, width) {
	const name = inner.toolName ?? "tool";
	const call = `call=${inner.toolCallId ?? "?"}`;
	const duration = inner.durationMs == null ? "" : ` ${intOf(inner.durationMs)}ms`;
	if (inner.error) {
		const reason = condense(messageOf(inner.error, "tool failed"), width);
		return `tool ${name} end ${call} failed${duration} error=${reason}`;
	}
	return `tool ${name} end ${call} ok${duration} out=${condense(inner.output, width)}`;
}

function usageLine(inner) {
	const parts = [`in=${intOf(inner.inputTokens)}`, `out=${intOf(inner.outputTokens)}`];
	if (inner.cacheReadTokens != null) parts.push(`cache_read=${intOf(inner.cacheReadTokens)}`);
	if (inner.cacheWriteTokens != null) parts.push(`cache_write=${intOf(inner.cacheWriteTokens)}`);
	if (inner.cost != null) parts.push(`cost=$${numOf(inner.cost)}`);
	return `usage ${parts.join(" ")}`;
}

function intOf(value) {
	const n = Number(value);
	return Number.isFinite(n) ? String(Math.trunc(n)) : "0";
}

function numOf(value) {
	const n = Number(value);
	return Number.isFinite(n) ? String(n) : "0";
}
