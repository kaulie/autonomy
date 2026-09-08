// Command models lists the Cursor models available to the configured API key.
//
// It talks to the Cursor SDK bridge and requires either a running bridge or a
// bridge binary (CURSOR_SDK_BRIDGE_BIN / third_party/bin/cursor-sdk-bridge).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := cursorsdk.NewClient()
	defer client.Close()

	models, err := client.Cursor().Models(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list models: %v\n", err)
		os.Exit(1)
	}
	if len(models) == 0 {
		fmt.Println("no models available to this account")
		return
	}

	for _, m := range models {
		fmt.Printf("%s\t%s\t%s\n", m.ID, m.DisplayName, m.Description)
	}
}
