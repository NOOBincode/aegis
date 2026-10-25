package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestTUIViewLayout(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		height      int
		expected    []string
		notExpected []string
	}{
		{
			name:   "wide with sidebar",
			width:  100,
			height: 30,
			expected: []string{
				"SAFE OPERATIONS", "CONTEXT", "SAFETY", "AEGIS", "中文结果",
			},
			notExpected: []string{"kind-aegis"},
		},
		{
			name:   "narrow without sidebar",
			width:  60,
			height: 20,
			expected: []string{
				"AEGIS", "enter send", "中文结果",
			},
			notExpected: []string{"CONTEXT", "kind-aegis"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AEGIS_TUI_WIDTH", strconv.Itoa(tt.width))
			t.Setenv("AEGIS_TUI_HEIGHT", strconv.Itoa(tt.height))
			ui, _ := newTestTUI(t, strings.NewReader(""))
			ui.entries = append(ui.entries, tuiEntry{role: tuiRoleAegis, text: "中文结果\x1b[2J\r"})
			ui.refreshTranscript(true)
			plain := ansi.Strip(ui.View())
			for _, want := range tt.expected {
				if !strings.Contains(plain, want) {
					t.Errorf("view missing %q\n%s", want, plain)
				}
			}
			for _, unwanted := range tt.notExpected {
				if strings.Contains(plain, unwanted) {
					t.Errorf("view unexpectedly contains %q\n%s", unwanted, plain)
				}
			}
			if strings.Contains(plain, "\x1b") {
				t.Fatalf("view contains terminal controls: %q", plain)
			}
			lines := strings.Split(plain, "\n")
			if len(lines) != tt.height {
				t.Fatalf("view height=%d, want %d\n%s", len(lines), tt.height, plain)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > tt.width {
					t.Fatalf("line width=%d, limit=%d: %q", ansi.StringWidth(line), tt.width, line)
				}
			}
			dump := filepath.Join(t.TempDir(), "frame.txt")
			t.Setenv("AEGIS_TUI_DUMP", dump)
			if err := ui.dump(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(dump) //nolint:gosec // 测试临时目录中的 dump 文件
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "\x1b") {
				t.Fatalf("dump contains ANSI: %q", raw)
			}
		})
	}
}

func TestTUIRejectsTinyTerminal(t *testing.T) {
	t.Setenv("AEGIS_TUI_WIDTH", "30")
	t.Setenv("AEGIS_TUI_HEIGHT", "10")
	ui, _ := newTestTUI(t, strings.NewReader(""))
	plain := ansi.Strip(ui.View())
	if !strings.Contains(plain, "Terminal too small") {
		t.Fatalf("view=%q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if ansi.StringWidth(line) > 30 {
			t.Fatalf("small view overflows: %q", line)
		}
	}
}

func TestTUIProgramHelpAndExit(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "frame.txt")
	ui, out := newTestTUI(t, strings.NewReader("/help\n/exit\n"))
	t.Setenv("AEGIS_TUI_DUMP", dump)
	if err := ui.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\033[?1049h") || !strings.Contains(out.String(), "\033[?1049l") {
		t.Fatalf("alternate screen was not entered and restored: %q", out.String())
	}
	raw, err := os.ReadFile(dump) //nolint:gosec // 测试临时目录中的 dump 文件
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/confirm") || !strings.Contains(string(raw), "Ctrl+C") {
		t.Fatalf("dump=%q", raw)
	}
}

func TestTUIAgentRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"artifacts":[{"parts":[{"kind":"text","text":"agent reply"}]}]}}`))
	}))
	defer srv.Close()
	ui, _ := newTestTUI(t, strings.NewReader(""))
	ui.endpoint = srv.URL
	cmd := submitForTest(ui, "hello")
	if cmd == nil || !ui.agentRunning {
		t.Fatal("agent request was not started")
	}
	runCmdForTest(ui, cmd)
	if !strings.Contains(entriesText(ui.entries), "agent reply") || ui.status != "idle" {
		t.Fatalf("status=%s entries=%s", ui.status, entriesText(ui.entries))
	}
}

func TestTUIAgentCancelAndStaleResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			http.Error(w, "request was not cancelled", http.StatusGatewayTimeout)
		}
	}))
	defer srv.Close()
	ui, _ := newTestTUI(t, strings.NewReader(""))
	ui.endpoint = srv.URL
	cmd := submitForTest(ui, "hello")
	if cmd == nil {
		t.Fatal("agent command is nil")
	}
	if second := submitForTest(ui, "another request"); second != nil {
		t.Fatal("second concurrent request was started")
	}
	if !strings.Contains(entriesText(ui.entries), "operation in progress") {
		t.Fatalf("entries=%s", entriesText(ui.entries))
	}
	submitForTest(ui, "/cancel")
	runCmdForTest(ui, cmd)
	if got := strings.Count(entriesText(ui.entries), "agent request cancelled"); got != 1 {
		t.Fatalf("cancel messages=%d entries=%s", got, entriesText(ui.entries))
	}
	if ui.agentRunning || strings.Contains(entriesText(ui.entries), "agent request failed") {
		t.Fatalf("stale result changed state: %+v", ui.entries)
	}
}

func TestTUIHelpAndEndpointValidation(t *testing.T) {
	ui, _ := newTestTUI(t, strings.NewReader(""))
	for _, want := range []string{"/gatekeeper", "/deny", "/cancel", "/confirm", "Ctrl+C", "Esc"} {
		if !strings.Contains(helpText(), want) {
			t.Errorf("help missing %q", want)
		}
	}
	originalAgent := ui.endpoint
	if _, err := ui.handle("/agent ftp://agent"); err != nil {
		t.Fatal(err)
	}
	if ui.endpoint != originalAgent {
		t.Fatalf("invalid agent endpoint was accepted: %s", ui.endpoint)
	}
	originalGateway := ui.gateway
	if _, err := ui.handle("/gatekeeper http://user:password@example.com"); err != nil {
		t.Fatal(err)
	}
	if ui.gateway != originalGateway {
		t.Fatalf("credentialed gateway endpoint was accepted: %s", ui.gateway)
	}
	if _, err := ui.handle(""); err != nil {
		t.Fatal(err)
	}
}

func TestScanDisplayPreservesStructuredOutput(t *testing.T) {
	jsonReport := `{"findings":[{"rootCause":"OOM"}]}`
	if got := scanDisplay(jsonReport + "\n"); got != jsonReport {
		t.Fatalf("scanDisplay=%q", got)
	}
	long := strings.Repeat("中", 40*1024)
	got := scanDisplay(long)
	if !strings.Contains(got, "output truncated") || !utf8.ValidString(got) {
		t.Fatalf("truncated output is invalid: len=%d", len(got))
	}
}

func TestTUINewSessionIsNotSavedOnStartup(t *testing.T) {
	ui, _ := newTestTUI(t, strings.NewReader(""))
	list, err := ui.store.list(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("startup created %d empty sessions", len(list))
	}
}

func TestTUIPersistErrorIsVisible(t *testing.T) {
	ui, _ := newTestTUI(t, strings.NewReader(""))
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui.store = &sessionStore{dir: filepath.Join(blocker, "sessions")}
	ui.hasActivity = true
	ui.persist()
	if !strings.Contains(entriesText(ui.entries), "session save failed") {
		t.Fatalf("entries=%s", entriesText(ui.entries))
	}
}

func TestTUISmallViewNeverExceedsHeight(t *testing.T) {
	for _, height := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(height), func(t *testing.T) {
			t.Setenv("AEGIS_TUI_WIDTH", "30")
			t.Setenv("AEGIS_TUI_HEIGHT", strconv.Itoa(height))
			ui, _ := newTestTUI(t, strings.NewReader(""))
			lines := strings.Split(ansi.Strip(ui.View()), "\n")
			if len(lines) > height {
				t.Fatalf("view has %d lines, limit %d", len(lines), height)
			}
		})
	}
}

func TestTUIAgentStartsSpinner(t *testing.T) {
	ui, _ := newTestTUI(t, strings.NewReader(""))
	cmd := submitForTest(ui, "hello")
	if cmd == nil {
		t.Fatal("agent command is nil")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("agent did not start request and spinner: %#v", batch)
	}
	ui.cancelRunning()
}

func TestTUIEscCancellationIsPersisted(t *testing.T) {
	ui, _ := newTestTUI(t, strings.NewReader(""))
	ui.hasActivity = true
	ui.pendingChange = &tuiChangeIntent{namespace: "shop", name: "api", from: 1, to: 3}
	ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
	record, err := ui.store.load(ui.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entriesText(record.Entries), "pending change discarded") {
		t.Fatalf("saved entries=%s", entriesText(record.Entries))
	}
}

func TestTUINewSessionAbortsWhenSaveFails(t *testing.T) {
	ui, _ := newTestTUI(t, strings.NewReader(""))
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui.store = &sessionStore{dir: filepath.Join(blocker, "sessions")}
	ui.hasActivity = true
	original := ui.sess
	if _, err := ui.handle("/new"); err != nil {
		t.Fatal(err)
	}
	if ui.sess != original || !strings.Contains(entriesText(ui.entries), "new session aborted") {
		t.Fatalf("session was replaced after save failure: %+v", ui.sess)
	}
}

func submitForTest(ui *tui, line string) tea.Cmd {
	ui.input.SetValue(line)
	_, cmd := ui.submit()
	return cmd
}

func runCmdForTest(ui *tui, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, nested := range batch {
			runCmdForTest(ui, nested)
		}
		return
	}
	ui.Update(msg)
}

func entriesText(entries []tuiEntry) string {
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(entry.text)
		b.WriteByte('\n')
	}
	return b.String()
}

func newTestTUI(t *testing.T, in io.Reader) (*tui, *strings.Builder) {
	t.Helper()
	t.Setenv("AEGIS_SESSION_DIR", t.TempDir())
	t.Setenv("AEGIS_TUI_DUMP", "")
	t.Setenv("AEGIS_AGENT_ENDPOINT", "http://127.0.0.1:18082/")
	if os.Getenv("AEGIS_TUI_WIDTH") == "" {
		t.Setenv("AEGIS_TUI_WIDTH", "100")
	}
	if os.Getenv("AEGIS_TUI_HEIGHT") == "" {
		t.Setenv("AEGIS_TUI_HEIGHT", "30")
	}
	cmd := newRootCmd()
	cmd.SetContext(context.Background())
	cmd.SetIn(in)
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return newTUI(cmd), out
}
