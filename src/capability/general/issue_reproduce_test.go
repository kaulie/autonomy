package general_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/general"
)

func TestIssueReproduceExceptionLogFromLogsURL(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"lines":["info ok","ERROR panic: boom","warn slow"]}`))
	}))
	defer srv.Close()

	cap := general.IssueReproduce{HTTPClient: srv.Client()}
	out, err := cap.Run(map[string]string{
		"mode":     "exception_log",
		"logs_url": srv.URL,
		"pattern":  `(?i)error|panic`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["mode"] != "exception_log" {
		t.Fatalf("mode=%q", out["mode"])
	}
	if !strings.Contains(out["log_text"], "panic") {
		t.Fatalf("log_text=%q", out["log_text"])
	}
	var lines []string
	if err := json.Unmarshal([]byte(out["logs"]), &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "ERROR panic: boom" {
		t.Fatalf("logs=%v", lines)
	}
}

func TestIssueReproduceExceptionLogFromLocalFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "server.log")
	content := strings.Join([]string{"line1", "line2", "ERROR failed"}, "\n")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cap := general.IssueReproduce{}
	out, err := cap.Run(map[string]string{
		"mode":     "exception_log",
		"log_path": logPath,
		"tail":     "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["line_count"] != "2" {
		t.Fatalf("line_count=%q", out["line_count"])
	}
	if !strings.Contains(out["log_text"], "ERROR failed") {
		t.Fatalf("log_text=%q", out["log_text"])
	}
}

func TestIssueReproduceUIImageURL(t *testing.T) {
	t.Parallel()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	cap := general.IssueReproduce{HTTPClient: srv.Client()}
	out, err := cap.Run(map[string]string{"mode": "ui", "url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if out["mode"] != "ui" || out["content_type"] != "image/png" {
		t.Fatalf("out=%v", out)
	}
	raw, err := base64.StdEncoding.DecodeString(out["screenshot_base64"])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:8]) != string(png[:8]) {
		t.Fatalf("png header mismatch")
	}
}

func TestIssueReproduceUIHeadlessInjected(t *testing.T) {
	t.Parallel()
	cap := general.IssueReproduce{
		HeadlessScreenshot: func(_ context.Context, pageURL string, _, _ int) ([]byte, string, error) {
			if pageURL != "https://example.test/page" {
				t.Fatalf("url=%q", pageURL)
			}
			return []byte("fakepng"), "image/png", nil
		},
	}
	out, err := cap.Run(map[string]string{"mode": "screenshot", "url": "https://example.test/page"})
	if err != nil {
		t.Fatal(err)
	}
	if out["screenshot_base64"] != base64.StdEncoding.EncodeToString([]byte("fakepng")) {
		t.Fatalf("out=%v", out)
	}
}
