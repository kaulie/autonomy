// Package all links in every harness this build ships.
//
// A harness registers itself with src/llmbackend when its package is linked in (the
// database/sql driver idiom), so the runtime just imports this one package:
//
//	import _ "github.com/kaulie/autonomy/src/llmbackend/all"
//
// Adding a harness is a new folder under src/llmbackend plus one line here — nothing in the
// core, the runtime or the prompts changes for it.
package all

import (
	_ "github.com/kaulie/autonomy/src/llmbackend/claude"
	// The harnesses this build can run agents on: one folder each, registered by importing
	// them (see the package comment).
	_ "github.com/kaulie/autonomy/src/llmbackend/cline"
	_ "github.com/kaulie/autonomy/src/llmbackend/codex"
	_ "github.com/kaulie/autonomy/src/llmbackend/cursor"
)
