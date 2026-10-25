package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type tui struct {
	cmd           *cobra.Command
	out           io.Writer
	in            io.Reader
	endpoint      string
	gateway       string
	approval      string
	actor         string
	namespace     string
	kubeconfig    string
	width         int
	height        int
	fixedWidth    bool
	fixedHeight   bool
	entries       []tuiEntry
	status        string
	store         *sessionStore
	sess          *sessionRecord
	storageErr    string
	resumePending bool
	pendingChange *tuiChangeIntent
	agentCancel   context.CancelFunc
	agentRunning  bool
	agentSeq      uint64
	actionCancel  context.CancelFunc
	actionRunning bool
	actionSeq     uint64
	hasActivity   bool
	input         textinput.Model
	viewport      viewport.Model
	spinner       spinner.Model
	styles        tuiStyles
}

type tuiEntry struct {
	role string
	text string
}

type tuiChangeIntent struct {
	namespace      string
	name           string
	from           int
	to             int
	idempotencyKey string
	attempted      bool
}

type agentResultMsg struct {
	id   uint64
	text string
	err  error
}

type actionResultMsg struct {
	id     uint64
	output tuiActionOutput
}

const (
	tuiRoleSystem = "system"
	tuiRoleYou    = "you"
	tuiRoleAegis  = "aegis"
	tuiRoleError  = "error"
)

func chooseInteractive(cmd *cobra.Command) error {
	isTTY := isTerminal(cmd.InOrStdin()) && isTerminal(cmd.OutOrStdout())
	if os.Getenv("AEGIS_TUI") == "force" || isTTY {
		return newTUI(cmd).run()
	}
	return newSession(cmd).run()
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func newTUI(cmd *cobra.Command) *tui {
	endpoint := strings.TrimSpace(os.Getenv("AEGIS_AGENT_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:18082/"
	}
	kubeconfig := strings.TrimSpace(os.Getenv("AEGIS_KUBECONFIG"))
	if kubeconfig == "" {
		kubeconfig = defaultKubeconfig()
	}
	gateway := strings.TrimSpace(os.Getenv("AEGIS_GATEKEEPER_ENDPOINT"))
	if gateway == "" {
		gateway = "http://127.0.0.1:18086/"
	}
	approval := strings.TrimSpace(os.Getenv("AEGIS_APPROVAL_ENDPOINT"))
	if approval == "" {
		approval = "http://127.0.0.1:18085/v1/approval"
	}
	actor := strings.TrimSpace(os.Getenv("AEGIS_ACTOR"))
	if actor == "" {
		actor = "tui-user"
	}
	width, fixedWidth := tuiDimension("AEGIS_TUI_WIDTH", 100)
	height, fixedHeight := tuiDimension("AEGIS_TUI_HEIGHT", 30)
	t := &tui{
		cmd: cmd, out: cmd.OutOrStdout(), in: cmd.InOrStdin(),
		endpoint: endpoint, gateway: gateway, approval: approval, actor: actor,
		namespace: os.Getenv("AEGIS_NAMESPACE"), kubeconfig: kubeconfig,
		width: width, height: height, fixedWidth: fixedWidth, fixedHeight: fixedHeight,
		status: "idle", styles: newTUIStyles(),
	}
	t.entries = []tuiEntry{
		{role: tuiRoleSystem, text: "Welcome to Aegis. Ask a read-only question or type /help."},
		{role: tuiRoleSystem, text: "Chat is read-only. Change intents always use the guarded approval flow."},
	}
	if store, err := newSessionStore(); err != nil {
		t.storageErr = err.Error()
		t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "session storage unavailable: " + err.Error()})
	} else {
		t.store = store
		t.sess = newSessionRecord(endpoint, t.namespace)
	}

	t.input = textinput.New()
	t.input.Prompt = "> "
	t.input.Placeholder = "Ask Aegis or type /help"
	t.input.CharLimit = 4096
	t.input.PromptStyle = t.styles.accent
	t.input.PlaceholderStyle = t.styles.muted
	t.viewport = viewport.New(width, height-6)
	t.spinner = spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(t.styles.warning))
	t.resize(width, height)
	return t
}

func tuiDimension(name string, fallback int) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback, false
	}
	return value, true
}

func (t *tui) Init() tea.Cmd {
	return tea.Batch(t.input.Focus(), tea.WindowSize())
}

func (t *tui) run() error {
	ctx := t.cmd.Context()
	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithInput(t.in),
		tea.WithOutput(t.out),
	}
	if ctx != nil {
		opts = append(opts, tea.WithContext(ctx))
	}
	final, err := tea.NewProgram(t, opts...).Run()
	if err != nil {
		if errors.Is(err, tea.ErrInterrupted) || (ctx != nil && ctx.Err() != nil) {
			return nil
		}
		return fmt.Errorf("run tui: %w", err)
	}
	if model, ok := final.(*tui); ok {
		return model.dump()
	}
	return t.dump()
}

func (t *tui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		width, height := msg.Width, msg.Height
		if t.fixedWidth {
			width = t.width
		}
		if t.fixedHeight {
			height = t.height
		}
		t.resize(width, height)
		return t, nil
	case agentResultMsg:
		t.finishAgent(msg)
		t.persist()
		t.refreshTranscript(false)
		return t, nil
	case actionResultMsg:
		t.finishAction(msg)
		t.persist()
		t.refreshTranscript(false)
		return t, nil
	case spinner.TickMsg:
		if !t.busy() {
			return t, nil
		}
		var cmd tea.Cmd
		t.spinner, cmd = t.spinner.Update(msg)
		return t, cmd
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			t.cancelRunning()
			t.persist()
			return t, tea.Quit
		case tea.KeyEsc:
			if t.busy() || t.pendingChange != nil {
				t.cancelPending()
				t.persist()
				t.refreshTranscript(false)
			}
			return t, nil
		case tea.KeyEnter, tea.KeyCtrlJ:
			return t.submit()
		case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
			var cmd tea.Cmd
			t.viewport, cmd = t.viewport.Update(msg)
			return t, cmd
		}
	}
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	return t, cmd
}

func (t *tui) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(t.input.Value())
	if line == "" {
		return t, nil
	}
	t.hasActivity = true
	t.input.Reset()
	t.entries = append(t.entries, tuiEntry{role: tuiRoleYou, text: line})
	cmd, err := t.handle(line)
	if errors.Is(err, errSessionExit) {
		t.refreshTranscript(true)
		return t, tea.Quit
	}
	if err != nil {
		t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: err.Error()})
	}
	t.persist()
	t.refreshTranscript(true)
	if cmd != nil {
		cmd = tea.Batch(cmd, t.spinner.Tick)
	}
	return t, cmd
}

func (t *tui) handle(line string) (tea.Cmd, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, nil
	}
	if t.busy() {
		switch fields[0] {
		case "/cancel", "/exit", "/quit", "/help", "/status":
		default:
			t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "operation in progress · use Esc or /cancel"})
			return nil, nil
		}
	}
	if strings.HasPrefix(line, "/") {
		switch fields[0] {
		case "/help":
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: helpText()})
		case "/status":
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: fmt.Sprintf(
				"session=%s  agent=%s  gateway=%s  approval=%s  namespace=%s  state=%s",
				sessionID(t), t.endpoint, t.gateway, t.approval, tuiNamespace(t.namespace), t.status,
			)})
		case "/sessions":
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: t.sessionListText()})
		case "/resume":
			if !t.persist() {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "session switch aborted: current transcript was not saved"})
				return nil, nil
			}
			if len(fields) < 2 {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: t.sessionListText()})
				return nil, nil
			}
			if err := t.resumeSession(fields[1]); err != nil {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: err.Error()})
				return nil, nil
			}
			t.pendingChange = nil
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "resumed session " + t.sess.ID + " · " + t.sess.Title})
		case "/new":
			if !t.persist() {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "new session aborted: current transcript was not saved"})
				return nil, nil
			}
			if t.store != nil {
				t.sess = newSessionRecord(t.endpoint, t.namespace)
			} else {
				t.sess = nil
			}
			t.entries = []tuiEntry{{role: tuiRoleSystem, text: "new session " + sessionID(t) + " · type /help for commands"}}
			t.resumePending = false
			t.pendingChange = nil
		case "/agent":
			if len(fields) != 2 || !validHTTPEndpoint(fields[1]) {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "usage: /agent <http(s)://host:port/> (without credentials)"})
				return nil, nil
			}
			t.endpoint = fields[1]
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "agent endpoint = " + t.endpoint})
		case "/namespace":
			if len(fields) < 2 {
				t.namespace = ""
			} else {
				t.namespace = fields[1]
			}
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "namespace = " + tuiNamespace(t.namespace)})
		case "/scan":
			action := t.newAction("scan")
			action.args = strings.Join(fields[1:], " ")
			return t.startAction(action), nil
		case "/gatekeeper":
			if len(fields) == 1 {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "gatekeeper = " + t.gateway})
				return nil, nil
			}
			if len(fields) != 2 || !validHTTPEndpoint(fields[1]) {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "usage: /gatekeeper <http(s)://host:port/> (without credentials)"})
				return nil, nil
			}
			t.gateway = fields[1]
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "gatekeeper = " + t.gateway})
		case "/change":
			return t.startChange(fields[1:]), nil
		case "/confirm":
			if t.pendingChange == nil {
				t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "no pending change to confirm"})
				return nil, nil
			}
			if t.pendingChange.idempotencyKey == "" {
				t.pendingChange.idempotencyKey = t.newChangeKey()
			}
			return t.startAction(t.changeAction(*t.pendingChange)), nil
		case "/cancel":
			t.cancelPending()
		case "/approve", "/deny", "/cr":
			action := t.newAction(fields[0][1:])
			action.fields = append([]string(nil), fields...)
			return t.startAction(action), nil
		case "/exit", "/quit":
			return nil, errSessionExit
		default:
			t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "unknown command " + fields[0] + " · /help"})
		}
		return nil, nil
	}

	t.pendingChange = nil
	if isScanLine(line) {
		action := t.newAction("scan")
		action.args = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "scan"))
		return t.startAction(action), nil
	}
	if ns, name, from, to, ok := parseScaleIntent(line); ok {
		t.pendingChange = &tuiChangeIntent{namespace: ns, name: name, from: from, to: to, idempotencyKey: t.newChangeKey()}
		t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: fmt.Sprintf(
			"scale preview: %s/%s replicas %d -> %d\nNo request sent. /confirm to submit through gatekeeper; Esc or /cancel to discard.",
			ns, name, from, to,
		)})
		return nil, nil
	}
	return t.startAgent(line), nil
}

func (t *tui) startChange(args []string) tea.Cmd {
	if len(args) != 4 {
		t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "usage: /change <namespace> <deployment> <from_replicas> <to_replicas>"})
		return nil
	}
	from, err1 := strconv.Atoi(args[2])
	to, err2 := strconv.Atoi(args[3])
	if err1 != nil || err2 != nil || from < 0 || to < 0 {
		t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "from/to replicas must be non-negative integers"})
		return nil
	}
	t.pendingChange = &tuiChangeIntent{
		namespace: args[0], name: args[1], from: from, to: to, idempotencyKey: t.newChangeKey(),
	}
	return t.startAction(t.changeAction(*t.pendingChange))
}

func (t *tui) changeAction(change tuiChangeIntent) tuiAction {
	action := t.newAction("change")
	action.change = change
	return action
}

func (t *tui) newChangeKey() string {
	return "tui-" + sessionID(t) + "-" + newSessionID()
}

func (t *tui) parentContext() context.Context {
	if ctx := t.cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func (t *tui) startAgent(line string) tea.Cmd {
	if t.busy() {
		t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "another operation is already in progress"})
		return nil
	}
	sendText := line
	if t.resumePending && t.sess != nil {
		prev := t.entries
		if len(prev) > 0 && prev[len(prev)-1].role == tuiRoleYou && prev[len(prev)-1].text == line {
			prev = prev[:len(prev)-1]
		}
		if resumeCtx := buildResumeContext(t.sess.ID, prev); resumeCtx != "" {
			sendText = resumeCtx + "\n\n" + line
		}
	}
	ctx, cancel := context.WithCancel(t.parentContext())
	t.agentSeq++
	requestID := t.agentSeq
	t.agentCancel = cancel
	t.agentRunning = true
	t.status = "thinking"
	endpoint := t.endpoint
	return func() tea.Msg {
		defer cancel()
		text, err := sendA2AMessage(ctx, endpoint, 240*time.Second, sendText)
		return agentResultMsg{id: requestID, text: text, err: err}
	}
}

func (t *tui) startAction(action tuiAction) tea.Cmd {
	if t.busy() {
		t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "another operation is already in progress"})
		return nil
	}
	if action.kind == "change" && t.pendingChange != nil && t.pendingChange.idempotencyKey == action.change.idempotencyKey {
		t.pendingChange.attempted = true
	}
	ctx, cancel := context.WithTimeout(t.parentContext(), action.timeout())
	t.actionSeq++
	requestID := t.actionSeq
	t.actionCancel = cancel
	t.actionRunning = true
	t.status = action.kind
	return func() tea.Msg {
		defer cancel()
		return actionResultMsg{id: requestID, output: action.execute(ctx)}
	}
}

func (t *tui) finishAgent(result agentResultMsg) {
	if !t.agentRunning || result.id != t.agentSeq {
		return
	}
	t.cancelAgent()
	if result.err != nil {
		if errors.Is(result.err, context.Canceled) {
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "agent request cancelled"})
			return
		}
		t.status = "error"
		t.entries = append(t.entries, tuiEntry{role: tuiRoleError, text: "agent request failed: " + result.err.Error() +
			"\nCheck /status or /agent <url>. /scan needs Kubernetes but does not use an LLM."})
		return
	}
	t.status = "idle"
	t.resumePending = false
	t.entries = append(t.entries, tuiEntry{role: tuiRoleAegis, text: result.text})
}

func (t *tui) finishAction(result actionResultMsg) {
	if !t.actionRunning || result.id != t.actionSeq {
		return
	}
	t.cancelAction()
	t.entries = append(t.entries, result.output.entries...)
	if result.output.changeKey != "" && t.pendingChange != nil && t.pendingChange.idempotencyKey == result.output.changeKey {
		t.pendingChange = nil
	}
}

func (t *tui) busy() bool {
	return t.agentRunning || t.actionRunning
}

func (t *tui) cancelAgent() {
	if t.agentCancel != nil {
		t.agentCancel()
		t.agentCancel = nil
	}
	t.agentRunning = false
	t.status = "idle"
}

func (t *tui) cancelAction() {
	if t.actionCancel != nil {
		t.actionCancel()
		t.actionCancel = nil
	}
	t.actionRunning = false
	t.status = "idle"
}

func (t *tui) cancelRunning() {
	t.cancelAgent()
	t.cancelAction()
}

func (t *tui) cancelPending() {
	if t.agentRunning {
		t.cancelAgent()
		t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "agent request cancelled"})
		return
	}
	if t.actionRunning {
		kind := t.status
		t.cancelAction()
		if t.pendingChange != nil {
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "operation cancelled; /confirm retries with the same idempotency key"})
		} else {
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: kind + " cancelled"})
		}
		return
	}
	if t.pendingChange != nil {
		attempted := t.pendingChange.attempted
		t.pendingChange = nil
		if attempted {
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "pending change discarded; a request may have been sent, review CR status before retrying"})
		} else {
			t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "pending change discarded; no request sent"})
		}
		return
	}
	t.entries = append(t.entries, tuiEntry{role: tuiRoleSystem, text: "nothing to cancel"})
}

func sessionID(t *tui) string {
	if t == nil || t.sess == nil {
		return "none"
	}
	return t.sess.ID
}
