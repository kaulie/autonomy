/**
 * Cross-restart continuation for a resident Cline session.
 *
 * A session's *liveness* does not survive its host: the Cline core runs inside this
 * bridge process, so a bridge restart takes the conversation with it (the SDK calls
 * that `session_not_found` and says a caller "has to replace the session rather than
 * keep retrying against it"). What *does* survive is the transcript: every session is
 * written to the Cline data dir (`<CLINE_DIR>/data/sessions/<sessionId>/`, default
 * `~/.cline/data/sessions/…`) as a manifest plus a `.messages.json`.
 *
 * So continuation is `readMessages` + a *new* session seeded with what it returns —
 * the SDK's own documented shape for resume/fork/compaction. Two measured facts decide
 * this design:
 *
 *   1. `start({config: {sessionId}})` does NOT load that session's history: it *starts*
 *      a session with that id, overwriting the stored transcript of a previous one.
 *   2. A new session seeded with `initialMessages: readMessages(old)` does continue the
 *      conversation (verified end to end: a fact stated in the old session is recalled
 *      in a fresh process).
 *
 * The seed is best effort by construction: a session with no transcript (the first
 * start, a retention cleanup, a session that was deleted) is not an error — it is a
 * conversation that has nothing to continue, and the caller starts fresh.
 */

/** Where a continuation's transcript came from, and what to seed the new session with. */
export function seedOf(sessionId, messages) {
	const id = typeof sessionId === "string" ? sessionId.trim() : "";
	if (!id || !Array.isArray(messages) || messages.length === 0) return null;
	return { from: id, messages };
}

/**
 * continuationSeed reads the transcript of the session a caller asked to continue, or
 * null when there is nothing to continue.
 *
 * `client` is the ClineCore (injected so a test can drive it without the SDK); `log` is
 * the bridge's logger. A read that fails is reported and treated as "nothing to
 * continue" — an unreadable transcript must never keep a run from starting.
 */
export async function continuationSeed(client, resumeSessionId, log = () => {}) {
	const id = typeof resumeSessionId === "string" ? resumeSessionId.trim() : "";
	if (!id) return null;
	if (!client || typeof client.readMessages !== "function") {
		log("warn", `resume session=${id} skipped: this client cannot read a session`);
		return null;
	}
	let messages;
	try {
		messages = await client.readMessages(id);
	} catch (err) {
		log("warn", `resume session=${id} transcript unreadable (${err?.message ?? err}); starting fresh`);
		return null;
	}
	const seed = seedOf(id, messages);
	if (!seed) {
		log("warn", `resume session=${id} has no transcript; starting fresh`);
		return null;
	}
	return seed;
}
