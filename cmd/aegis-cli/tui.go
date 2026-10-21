package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// tui 是全屏终端视图；默认在 TTY 下启用，AEGIS_TUI=force 可在管道/截图环境强制渲染。
// 它只负责呈现与输入路由，scan/Agent 仍走同一 CLI 函数，不新增业务路径。
type tui struct {
	cmd           *cobra.Command
	out           io.Writer
	in            io.Reader
	endpoint      string
	namespace     string
	kubeconfig    string
	width         int
	height        int
	entries       []tuiEntry
	status        string
	store         *sessionStore
	sess          *sessionRecord
	storageErr    string
	resumePending bool
}

type tuiEntry struct {
	role string
	text string
}

const (
	tuiRoleSystem = "system"
	tuiRoleYou    = "you"
	tuiRoleAegis  = "aegis"
	tuiRoleError  = "error"
)

func chooseInteractive(cmd *cobra.Command) error {
	if os.Getenv("AEGIS_TUI") == "force" || isTerminal(cmd.OutOrStdout()) {
		return newTUI(cmd).run()
	}
	return newSession(cmd).run()
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func newTUI(cmd *cobra.Command) *tui {
	endpoint := strings.TrimSpace(os.Getenv("AEGIS_AGENT_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:18082/"
	}
	kc := strings.TrimSpace(os.Getenv("AEGIS_KUBECONFIG"))
	if kc == "" {
		kc = defaultKubeconfig()
	}
	w, h := 100, 30
	_, _ = fmt.Sscanf(os.Getenv("AEGIS_TUI_WIDTH"), "%d", &w)
	_, _ = fmt.Sscanf(os.Getenv("AEGIS_TUI_HEIGHT"), "%d", &h)
	if w < 60 {
		w = 60
	}
	if h < 16 {
		h = 16
	}
	t := &tui{cmd: cmd, out: cmd.OutOrStdout(), in: cmd.InOrStdin(), endpoint: endpoint,
		namespace: os.Getenv("AEGIS_NAMESPACE"), kubeconfig: kc, width: w, height: h, status: "ready"}
	t.entries = append(t.entries,
		tuiEntry{tuiRoleSystem, "security operations console · read-only by default"},
		tuiEntry{tuiRoleSystem, "Type a request, or use /scan /status /agent /namespace /help /exit."},
	)
	if st, err := newSessionStore(); err != nil {
		t.storageErr = err.Error()
		t.entries = append(t.entries, tuiEntry{tuiRoleError, "session storage unavailable: " + err.Error()})
	} else {
		t.store = st
		t.sess = newSessionRecord(endpoint, t.namespace)
		t.sess.Entries = t.entries
		_ = t.store.save(t.sess)
	}
	return t
}

func (t *tui) run() error {
	_, _ = fmt.Fprint(t.out, "\033[?1049h\033[?25l\033[2J")
	defer func() { _, _ = fmt.Fprint(t.out, "\033[?25h\033[?1049l") }()
	t.render()
	sc := bufio.NewScanner(t.in)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for {
		if !sc.Scan() {
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		t.entries = append(t.entries, tuiEntry{tuiRoleYou, line})
		if err := t.handle(line); err != nil {
			t.entries = append(t.entries, tuiEntry{tuiRoleError, err.Error()})
		}
		t.persist()
		t.render()
		if line == "/exit" || line == "/quit" {
			return nil
		}
	}
}

func (t *tui) handle(line string) error {
	if strings.HasPrefix(line, "/") {
		fields := strings.Fields(line)
		switch fields[0] {
		case "/help":
			t.entries = append(t.entries, tuiEntry{tuiRoleSystem, helpText()})
			return nil
		case "/status":
			id := "no-session"
			if t.sess != nil {
				id = t.sess.ID
			}
			t.entries = append(t.entries, tuiEntry{tuiRoleSystem, fmt.Sprintf("session=%s  agent=%s  namespace=%s  kubeconfig=%s", id, t.endpoint, tuiNamespace(t.namespace), t.kubeconfig)})
			return nil
		case "/sessions":
			t.entries = append(t.entries, tuiEntry{tuiRoleSystem, t.sessionListText()})
			return nil
		case "/resume":
			t.persist()
			if len(fields) < 2 {
				t.entries = append(t.entries, tuiEntry{tuiRoleSystem, t.sessionListText()})
				return nil
			}
			if err := t.resumeSession(fields[1]); err != nil {
				t.entries = append(t.entries, tuiEntry{tuiRoleError, err.Error()})
				return nil
			}
			t.entries = append(t.entries, tuiEntry{tuiRoleSystem, "resumed session " + t.sess.ID + " · " + t.sess.Title})
			return nil
		case "/new":
			t.persist()
			if t.store != nil {
				t.sess = newSessionRecord(t.endpoint, t.namespace)
				t.sess.Entries = t.entries
				t.entries = []tuiEntry{
					{tuiRoleSystem, "security operations console · read-only by default"},
					{tuiRoleSystem, "new session " + t.sess.ID},
				}
				t.sess.Entries = t.entries
				t.resumePending = false
				_ = t.store.save(t.sess)
			}
			return nil
		case "/agent":
			if len(fields) < 2 {
				t.entries = append(t.entries, tuiEntry{tuiRoleError, "usage: /agent <http(s)://host:port/>"})
				return nil
			}
			t.endpoint = fields[1]
			t.entries = append(t.entries, tuiEntry{tuiRoleSystem, "agent endpoint = " + t.endpoint})
			return nil
		case "/namespace":
			if len(fields) < 2 {
				t.namespace = ""
				t.entries = append(t.entries, tuiEntry{tuiRoleSystem, "namespace = ALL"})
			} else {
				t.namespace = fields[1]
				t.entries = append(t.entries, tuiEntry{tuiRoleSystem, "namespace = " + t.namespace})
			}
			return nil
		case "/scan":
			return t.scan(strings.Join(fields[1:], " "))
		case "/exit", "/quit":
			return nil
		default:
			t.entries = append(t.entries, tuiEntry{tuiRoleError, "unknown command " + fields[0] + " · /help"})
			return nil
		}
	}
	if isScanLine(line) {
		return t.scan(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "scan")))
	}
	t.status = "thinking"
	t.render()
	sendText := line
	if t.resumePending && t.sess != nil {
		prev := t.entries
		if len(prev) > 0 {
			prev = prev[:len(prev)-1]
		}
		if ctx := buildResumeContext(t.sess.ID, prev); ctx != "" {
			sendText = ctx + "\n\n" + line
		}
		t.resumePending = false
	}
	text, err := sendA2AMessage(t.cmd.Context(), t.endpoint, 240*time.Second, sendText)
	t.status = "ready"
	if err != nil {
		t.entries = append(t.entries, tuiEntry{tuiRoleError, "agent unavailable: " + err.Error() + " · /scan works locally"})
		return nil
	}
	t.entries = append(t.entries, tuiEntry{tuiRoleAegis, text})
	return nil
}

func (t *tui) scan(args string) error {
	ns := t.namespace
	format := ""
	fields := strings.Fields(args)
	for i := 0; i < len(fields); i++ {
		a := fields[i]
		switch {
		case (a == "-n" || a == "--namespace") && i+1 < len(fields):
			ns = fields[i+1]
			i++
		case strings.HasPrefix(a, "--namespace="):
			ns = strings.TrimPrefix(a, "--namespace=")
		case a == "json" || a == "text" || a == "sarif":
			format = a
		case !strings.HasPrefix(a, "-"):
			ns = a
		}
	}
	cmdArgs := []string{"--kubeconfig", t.kubeconfig}
	if ns != "" {
		cmdArgs = append(cmdArgs, "--namespace", ns)
	}
	if format != "" {
		cmdArgs = append(cmdArgs, "--format", format)
	}
	var buf bytes.Buffer
	scanCmd := newScanCmd()
	scanCmd.SetContext(t.cmd.Context())
	scanCmd.SetOut(&buf)
	scanCmd.SetErr(&buf)
	scanCmd.SetArgs(cmdArgs)
	if err := scanCmd.Execute(); err != nil {
		t.entries = append(t.entries, tuiEntry{tuiRoleError, "scan failed: " + err.Error()})
		return nil
	}
	t.entries = append(t.entries, tuiEntry{tuiRoleAegis, compactScan(buf.String())})
	return nil
}

func compactScan(out string) string {
	// TUI is English by default so terminal screenshots do not depend on CJK fonts.
	// Keep the deterministic counts, drop raw Chinese finding lines.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range lines {
		if strings.Contains(l, "扫描 Pod") {
			replaced := strings.NewReplacer(
				"扫描 Pod", "Scanned ",
				" 个 · 命中 ", " pods · ",
				" 条（严重 ", " findings (critical ",
				" / 警告 ", " / warning ",
				" / 提示 ", " / info ",
				"）· 规则结案 ", ") · rule-closed ",
				" 条", "",
			).Replace(strings.TrimSpace(l))
			return replaced
		}
	}
	return "scan completed: no findings"
}

func asciiUI(s string) string {
	// The TUI itself is ASCII-first so screenshots do not depend on a CJK font.
	// Real terminal users still receive the Agent's original text in the plain
	// session; the full-color TUI intentionally normalizes punctuation.
	repl := strings.NewReplacer(
		"·", "|", "—", "-", "–", "-", "…", "...", "“", "\"", "”", "\"",
		"‘", "'", "’", "'", "→", "->", "✓", "[ok]", "⚠", "!", "｜", "|",
	)
	return repl.Replace(s)
}

func tuiNamespace(ns string) string {
	if ns == "" {
		return "ALL"
	}
	return ns
}

func (t *tui) render() {
	var b strings.Builder
	plain := &bytes.Buffer{}
	line := strings.Repeat("-", t.width)
	write := func(s string) { b.WriteString(s); b.WriteByte('\n') }
	plainWrite := func(s string) { plain.WriteString(s); plain.WriteByte('\n') }

	title := " AEGIS  //  SECURITY OPERATIONS CONSOLE"
	tag := "[ READ-ONLY ]"
	head := pad(title, t.width-len(tag)) + tag
	write("\033[36;1m" + head + "\033[0m")
	plainWrite(head)
	status := fmt.Sprintf(" session %s  |  cluster kind-aegis  |  ns %s  |  agent %s", sessionID(t), tuiNamespace(t.namespace), t.status)
	write("\033[2m" + pad(status, t.width) + "\033[0m")
	plainWrite(pad(status, t.width))
	write("\033[2m" + line + "\033[0m")
	plainWrite(line)

	bodyH := t.height - 6
	if bodyH < 8 {
		bodyH = 8
	}
	mainW := t.width - 32
	if mainW < 30 {
		mainW = 30
	}
	var left []string
	var leftPlain []string
	for _, e := range t.entries {
		prefix, color := "  SYS  | ", "\033[2m"
		switch e.role {
		case tuiRoleYou:
			prefix, color = "  YOU  | ", "\033[32;1m"
		case tuiRoleAegis:
			prefix, color = "  AEGIS| ", "\033[37m"
		case tuiRoleError:
			prefix, color = "  ERR  | ", "\033[31;1m"
		}
		for _, l := range wrapText(asciiUI(e.text), mainW-10) {
			left = append(left, color+prefix+l+"\033[0m")
			leftPlain = append(leftPlain, prefix+l)
		}
	}
	if len(left) > bodyH {
		left = left[len(left)-bodyH:]
		leftPlain = leftPlain[len(leftPlain)-bodyH:]
	}
	for len(left) < bodyH {
		left = append([]string{""}, left...)
		leftPlain = append([]string{""}, leftPlain...)
	}

	side := []string{
		"  QUICK START",
		"  /scan [ns]",
		"  /status",
		"  /agent <url>",
		"  /namespace <ns>",
		"",
		"  TARGET",
		"  cluster  kind-aegis",
		"  namespace " + tuiNamespace(t.namespace),
		"",
		"  CONNECTION",
		"  " + shortEndpoint(t.endpoint),
		"  status " + t.status,
		"",
		"  SAFETY",
		"  read-only console",
		"  writes -> request_change",
		"  R2 -> human approval",
		"",
		"  SESSION " + sessionID(t),
		"  " + shortTitle(t.sessTitle()),
		"  /sessions  /resume <id>",
	}
	for i := 0; i < bodyH; i++ {
		l := ""
		lp := ""
		if i < len(left) {
			l = left[i]
			lp = leftPlain[i]
		}
		sp := mainW - len([]rune(lp))
		if sp < 0 {
			sp = 0
		}
		sideLine := ""
		if i < len(side) {
			sideLine = side[i]
		}
		row := l + strings.Repeat(" ", sp) + " | " + sideLine
		plainRow := lp + strings.Repeat(" ", sp) + " | " + sideLine
		write(row)
		plainWrite(plainRow)
	}
	write("\033[2m" + line + "\033[0m")
	plainWrite(line)
	write("\033[2m /scan  /status  /sessions  /resume  /new  /help  /exit             safety: read-only, writes require approval\033[0m")
	plainWrite(" /scan  /status  /sessions  /resume  /new  /help  /exit             safety: read-only, writes require approval")
	write("\033[33;1m > \033[0m")
	plainWrite(" > ")

	_, _ = fmt.Fprint(t.out, b.String())
	if path := os.Getenv("AEGIS_TUI_DUMP"); path != "" {
		_ = os.WriteFile(path, plain.Bytes(), 0o600) //nolint:gosec // 显式调试截图路径
	}
}

func visible(s string) string {
	for {
		i := strings.Index(s, "\033[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+1:]
	}
}

func pad(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func wrapText(s string, n int) []string {
	if n < 8 {
		n = 8
	}
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		if raw == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(raw)
		line := ""
		for _, w := range words {
			if line == "" {
				for len([]rune(w)) > n {
					r := []rune(w)
					out = append(out, string(r[:n]))
					w = string(r[n:])
				}
				line = w
				continue
			}
			if len([]rune(line))+1+len([]rune(w)) <= n {
				line += " " + w
				continue
			}
			out = append(out, line)
			line = w
			for len([]rune(line)) > n {
				r := []rune(line)
				out = append(out, string(r[:n]))
				line = string(r[n:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func (t *tui) sessTitle() string {
	if t == nil || t.sess == nil || t.sess.Title == "" {
		return "new session"
	}
	return t.sess.Title
}

func shortTitle(s string) string {
	r := []rune(s)
	if len(r) > 26 {
		return string(r[:25]) + "~"
	}
	return s
}

func shortEndpoint(endpoint string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	s = strings.TrimSuffix(s, "/")
	if len([]rune(s)) > 26 {
		s = string([]rune(s)[:25]) + "~"
	}
	return s
}

func sessionID(t *tui) string {
	if t == nil || t.sess == nil {
		return "none"
	}
	return t.sess.ID
}

func helpText() string {
	return "commands: /scan [ns] [json|text|sarif] · /status · /agent <url> · /namespace <ns> · /sessions · /resume <id> · /new · /help · /exit"
}
