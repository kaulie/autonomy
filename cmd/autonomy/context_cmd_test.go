package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestContextCLIPointsAtKnowledgeBase(t *testing.T) {
	for _, args := range [][]string{
		{"context"},
		{"context", "register", "--project", "p", "--name", "x"},
		{"context", "sync", "ctx-1"},
	} {
		var stdout, stderr bytes.Buffer
		if code := cli(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "knowledge-base") {
			t.Errorf("%v: stderr %q", args, stderr.String())
		}
	}
}
