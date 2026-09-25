package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func (s *session) Prompt(ctx context.Context, prompt string, mode llmbackend.Mode, sink func(llmbackend.Event)) (out string, result llmbackend.RunResult, err error) {
	result.StartedAt = time.Now()
	result.Status = llmbackend.StatusError
	defer func() {
		result.EndedAt = time.Now()
		result.DurationMS = result.EndedAt.Sub(result.StartedAt).Milliseconds()
		result.RawOutput = out
		if err != nil {
			result.Status = llmbackend.StatusError
			result.ErrorMessage = err.Error()
		}
		if ctx.Err() != nil {
			err = ctx.Err()
			result.Status = llmbackend.StatusCancelled
			result.ErrorMessage = err.Error()
		}
	}()
	if _, err = s.Attach(ctx, mode); err != nil {
		return
	}
	s.from, s.resumed = s.id, s.id != "" && !s.host.Facts().Ephemeral
	cmd := exec.CommandContext(ctx, binary(), s.args()...)
	cmd.Dir, cmd.Env, cmd.Stdin = s.cwd, environment(s.host.Facts().Creds), strings.NewReader(prompt)
	// Bound inherited pipe lifetimes if a tool outlives the CLI on cancellation.
	cmd.WaitDelay = 2 * time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = pipe.Close() })
	defer stop()
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	finished := false
	for scanner.Scan() {
		var native map[string]any
		if err = json.Unmarshal(scanner.Bytes(), &native); err != nil {
			err = fmt.Errorf("claude: invalid stream JSON: %w", err)
			break
		}
		s.record(llmbackend.PayloadString(native, "session_id"))
		for _, ev := range events(native) {
			result.EventCount++
			if sink != nil {
				sink(ev)
			}
		}
		if llmbackend.PayloadString(native, "type") != "result" {
			continue
		}
		finished = true
		out = strings.TrimSpace(llmbackend.PayloadString(native, "result"))
		result.ProviderRunID, result.LLMAgentID = s.id, s.id
		result.Usage = usage(native)
		failed, _ := native["is_error"].(bool)
		if failed || llmbackend.PayloadString(native, "subtype") != "success" {
			err = fmt.Errorf("claude run failed (%s)", llmbackend.PayloadString(native, "subtype"))
			break
		}
		result.Status = llmbackend.StatusFinished
	}
	if err == nil {
		err = scanner.Err()
	}
	if err != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err == nil && waitErr != nil {
		err = fmt.Errorf("claude CLI: %w", waitErr)
	}
	if err == nil && !finished {
		err = fmt.Errorf("claude stream ended without a result")
	}
	if err == nil && out == "" {
		err = fmt.Errorf("claude returned an empty response")
	}
	return
}

func usage(native map[string]any) llmbackend.Usage {
	u := llmbackend.PayloadMap(native, "usage")
	n := func(key string) int64 { v, _ := llmbackend.PayloadNumber(u, key); return int64(v) }
	result := llmbackend.Usage{InputTokens: n("input_tokens"), OutputTokens: n("output_tokens"), CacheReadTokens: n("cache_read_input_tokens"), CacheWriteTokens: n("cache_creation_input_tokens")}
	result.TotalTokens = result.InputTokens + result.OutputTokens + result.CacheReadTokens + result.CacheWriteTokens
	if cost, ok := llmbackend.PayloadNumber(native, "total_cost_usd"); ok {
		result.CostKnown, result.CostCents = true, cost*100
	}
	return result
}
