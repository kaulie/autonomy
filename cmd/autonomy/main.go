package main

import (
	"fmt"
	"os"
	"time"

	autonomy "github.com/kaulie/autonomy/src"
)

func main() {

	a := autonomy.BootstrapAutonomy()
	task := &autonomy.Task{
		ID:          "1",
		Description: "Develop a new feature",
		Target:      "1",
		Status:      "pending",
		Contract:    autonomy.Contract{ExpectedState: "changed"},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	err := a.Run(task)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
