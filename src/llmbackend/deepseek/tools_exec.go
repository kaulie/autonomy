package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func (t toolbox) readFile(args map[string]any) (string, error) {
	full, err := t.resolve(str(args, "path"))
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	offset, limit := num(args, "offset"), num(args, "limit")
	if offset <= 0 && limit <= 0 {
		return string(raw), nil
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if offset < 1 {
		offset = 1
	}
	if offset > len(lines) {
		return "", nil
	}
	end := len(lines)
	if limit > 0 && offset-1+limit < end {
		end = offset - 1 + limit
	}
	return strings.Join(lines[offset-1:end], ""), nil
}

func (t toolbox) writeFile(args map[string]any) (string, error) {
	full, err := t.resolve(str(args, "path"))
	if err != nil {
		return "", err
	}
	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("content is required")
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", t.display(full), len(content)), nil
}

func (t toolbox) editFile(args map[string]any) (string, error) {
	full, err := t.resolve(str(args, "path"))
	if err != nil {
		return "", err
	}
	oldS, newS := str(args, "old_string"), str(args, "new_string")
	if oldS == "" {
		return "", fmt.Errorf("old_string is required")
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	text := string(raw)
	count := strings.Count(text, oldS)
	all, _ := args["replace_all"].(bool)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string not found in %s", t.display(full))
	case count > 1 && !all:
		return "", fmt.Errorf("old_string occurs %d times in %s: make it unique or set replace_all", count, t.display(full))
	}
	n := 1
	if all {
		n = -1
	}
	info, _ := os.Stat(full)
	perm := fs.FileMode(0o644)
	if info != nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(full, []byte(strings.Replace(text, oldS, newS, n)), perm); err != nil {
		return "", err
	}
	if !all {
		count = 1
	}
	return fmt.Sprintf("edited %s (%d replacement(s))", t.display(full), count), nil
}

// skipDir is a directory a listing or search never descends into.
func skipDir(name string) bool {
	return name == ".git" || name == "node_modules"
}

// walk visits the regular files under root (a file root visits just itself).
func (t toolbox) walk(root, glob string, visit func(path string) bool) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, d.Name()); !ok {
				return nil
			}
		}
		if !visit(path) {
			return filepath.SkipAll
		}
		return nil
	})
}

func (t toolbox) listFiles(args map[string]any) (string, error) {
	root, err := t.resolve(str(args, "path"))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(root); err != nil {
		return "", err
	}
	var files []string
	more := false
	err = t.walk(root, str(args, "pattern"), func(path string) bool {
		if len(files) >= maxListed {
			more = true
			return false
		}
		files = append(files, t.display(path))
		return true
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	out := strings.Join(files, "\n")
	if more {
		out += fmt.Sprintf("\n… [stopped at %d files]", maxListed)
	}
	if out == "" {
		out = "(no files)"
	}
	return out, nil
}

func (t toolbox) searchFiles(args map[string]any) (string, error) {
	re, err := regexp.Compile(str(args, "pattern"))
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	root, err := t.resolve(str(args, "path"))
	if err != nil {
		return "", err
	}
	var hits []string
	err = t.walk(root, str(args, "glob"), func(path string) bool {
		f, err := os.Open(path)
		if err != nil {
			return true
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for line := 1; sc.Scan(); line++ {
			text := sc.Bytes()
			if bytes.IndexByte(text, 0) >= 0 {
				return true // binary
			}
			if re.Match(text) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", t.display(path), line, text))
				if len(hits) >= maxListed {
					return false
				}
			}
		}
		return true
	})
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "(no matches)", nil
	}
	return strings.Join(hits, "\n"), nil
}

func (t toolbox) runShell(ctx context.Context, args map[string]any) (string, bool) {
	command := str(args, "command")
	if strings.TrimSpace(command) == "" {
		return "command is required", true
	}
	timeout := defaultShellTimeout
	if s := num(args, "timeout_seconds"); s > 0 {
		timeout = time.Duration(s) * time.Second
	}
	if timeout > maxShellTimeout {
		timeout = maxShellTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-c", command)
	cmd.Dir = t.root
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := truncate(buf.String())
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		return out + fmt.Sprintf("\n[timed out after %s]", timeout), true
	case err != nil:
		code := -1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
		return out + fmt.Sprintf("\n[exit code %d]", code), true
	}
	return out + "\n[exit code 0]", false
}
