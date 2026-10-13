package general

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// headlessBrowserScreenshot captures pageURL with a Chromium-based browser when configured.
// ISSUE_REPRODUCE_BROWSER or CHROMIUM_PATH must point at a chromium/chrome executable.
func headlessBrowserScreenshot(ctx context.Context, pageURL string, width, height int) ([]byte, string, error) {
	bin := strings.TrimSpace(os.Getenv("ISSUE_REPRODUCE_BROWSER"))
	if bin == "" {
		bin = strings.TrimSpace(os.Getenv("CHROMIUM_PATH"))
	}
	if bin == "" {
		return nil, "", fmt.Errorf("url is not an image and no headless browser configured (set ISSUE_REPRODUCE_BROWSER or CHROMIUM_PATH)")
	}
	tmp, err := os.CreateTemp("", "issue-reproduce-*.png")
	if err != nil {
		return nil, "", err
	}
	outPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(outPath) }()

	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--screenshot=" + outPath,
		pageURL,
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, bin, args...)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return nil, "", fmt.Errorf("headless screenshot: %w (%s)", err, snippet)
	}
	data, err := os.ReadFile(filepath.Clean(outPath))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("headless screenshot produced empty file")
	}
	return data, "image/png", nil
}
