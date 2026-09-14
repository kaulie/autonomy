/**
 * Cline bridge configuration helpers.
 *
 * The Cline SDK requires an explicit providerId and modelId (it dereferences
 * both while assembling a session) and requires a non-empty system prompt. The
 * provider credentials themselves are optional: the SDK also reads whatever
 * `cline auth` saved, so a machine that already authenticated needs no key here.
 *
 * When the caller does not configure provider/model, the bridge falls back to
 * the provider saved by `cline auth` (settings/providers.json), which keeps
 * `AUTONOMY_LLM_BACKEND=cline` a one-variable switch for anyone who already ran
 * `cline auth`.
 */
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export const PROTOCOL = "cline-bridge/1";
export const DEFAULT_MODE = "yolo";
export const DEFAULT_SYSTEM_PROMPT =
	"You are an autonomous coding agent working inside the configured workspace. " +
	"Use the available tools to complete the task, verifying your work, and finish " +
	"with a concise summary of what you changed and why.";

/**
 * Candidate providers.json paths, most specific first. AUTONOMY_CLINE_DATA_DIR /
 * CLINE_DATA_DIR / CLINE_DIR point at the Cline data directory (or at ~/.cline).
 */
export function clineConfigCandidates(env = process.env, home = os.homedir()) {
	const roots = [];
	const explicit = (env.AUTONOMY_CLINE_DATA_DIR || env.CLINE_DATA_DIR || env.CLINE_DIR || "").trim();
	if (explicit) roots.push(explicit, path.join(explicit, "data"));
	roots.push(path.join(home, ".cline", "data"), path.join(home, ".cline"));
	return roots.map((root) => path.join(root, "settings", "providers.json"));
}

/**
 * Resolve the provider/model `cline auth` saved. Returns null when nothing
 * usable is configured. IO is injectable so this stays unit-testable.
 */
export function resolveClineDefaults({ env = process.env, home = os.homedir(), readFile = fs.readFileSync } = {}) {
	for (const file of clineConfigCandidates(env, home)) {
		let parsed;
		try {
			parsed = JSON.parse(readFile(file, "utf8"));
		} catch {
			continue;
		}
		const providerId = typeof parsed?.lastUsedProvider === "string" ? parsed.lastUsedProvider.trim() : "";
		const settings = providerId ? parsed?.providers?.[providerId]?.settings : undefined;
		const modelId = typeof settings?.model === "string" ? settings.model.trim() : "";
		if (providerId && modelId) {
			return { providerId, modelId, source: file };
		}
	}
	return null;
}

/**
 * coerceText renders a provider value as text: strings pass through, numbers and
 * booleans become their literal form, objects/arrays become compact JSON, and
 * null/undefined become "". The SDK occasionally hands back a non-string where a
 * string is expected (a structured error, for example), and the Go client must
 * never fail to decode a finished run because of that.
 */
export function coerceText(value) {
	if (value == null) return "";
	if (typeof value === "string") return value;
	if (typeof value === "number" || typeof value === "boolean") return String(value);
	try {
		return JSON.stringify(value);
	} catch {
		return String(value);
	}
}

/**
 * messageOf picks a human-readable message out of an error-ish value: a plain
 * string wins, otherwise the usual message keys are preferred and anything else
 * degrades to compact JSON (or the fallback).
 */
export function messageOf(value, fallback = "") {
	if (typeof value === "string") {
		return value.trim() !== "" ? value : fallback;
	}
	if (value && typeof value === "object") {
		for (const key of ["message", "error", "detail", "reason", "code"]) {
			const candidate = value[key];
			if (typeof candidate === "string" && candidate.trim() !== "") return candidate;
			if (candidate && typeof candidate === "object") {
				const nested = messageOf(candidate, "");
				if (nested !== "") return nested;
			}
		}
		const text = coerceText(value);
		return text !== "" && text !== "{}" ? text : fallback;
	}
	const text = coerceText(value);
	return text !== "" ? text : fallback;
}
