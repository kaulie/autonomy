package main

import (
	"fmt"
	"io"
)

const contextGone = `knowledge is maintained on knowledge-base, not this runtime.
Register, bind and search there (KNOWLEDGE_BASE_URL, default 49.234.45.173:4360).
`

// contextCLI used to register and sync context on this runtime. That work
// moved to knowledge-base; this command only points there.
func contextCLI(_ []string, _, stderr io.Writer) int {
	fmt.Fprint(stderr, contextGone)
	return 2
}
