import test from "node:test";
import assert from "node:assert/strict";

import { continuationSeed, seedOf } from "./resume.mjs";

const transcript = [
	{ id: "msg_1", role: "user", content: [{ type: "text", text: "remember 4321" }] },
	{ id: "msg_2", role: "assistant", content: [{ type: "text", text: "OK" }] },
];

// A seed is a transcript that exists: the id and the messages, or nothing.
test("seedOf keeps a session with a transcript and refuses the rest", () => {
	assert.deepEqual(seedOf("cls-a", transcript), { from: "cls-a", messages: transcript });
	assert.equal(seedOf("cls-a", []), null);
	assert.equal(seedOf("", transcript), null);
	assert.equal(seedOf("cls-a", undefined), null);
	assert.equal(seedOf("  cls-a  ", transcript)?.from, "cls-a");
});

// The seed a restart continues from: the old session's messages, read back before the
// new session starts.
test("continuationSeed reads the transcript of the session asked for", async () => {
	const read = [];
	const client = { readMessages: async (id) => (read.push(id), transcript) };
	const seed = await continuationSeed(client, "cls-a");
	assert.deepEqual(seed, { from: "cls-a", messages: transcript });
	assert.deepEqual(read, ["cls-a"]);
});

// No id is the first start of a session, not a failed resume: no read, no warning.
test("continuationSeed does nothing without a session to continue", async () => {
	let reads = 0;
	const lines = [];
	const client = { readMessages: async () => (reads++, transcript) };
	assert.equal(await continuationSeed(client, "", (...a) => lines.push(a)), null);
	assert.equal(await continuationSeed(client, undefined, (...a) => lines.push(a)), null);
	assert.equal(reads, 0);
	assert.deepEqual(lines, []);
});

// An unreadable or empty transcript is "nothing to continue" — a run must still start.
test("continuationSeed starts fresh when the transcript is gone", async () => {
	const lines = [];
	const log = (level, message) => lines.push(`${level}: ${message}`);
	const broken = {
		readMessages: async () => {
			throw new Error("session_not_found: session not found: cls-gone");
		},
	};
	assert.equal(await continuationSeed(broken, "cls-gone", log), null);
	assert.match(lines[0], /transcript unreadable/);

	const empty = { readMessages: async () => [] };
	assert.equal(await continuationSeed(empty, "cls-empty", log), null);
	assert.match(lines[1], /no transcript/);

	// A client that cannot read at all (an older SDK) is the same story.
	assert.equal(await continuationSeed({}, "cls-a", log), null);
	assert.match(lines[2], /cannot read a session/);
});
