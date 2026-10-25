package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	tuiChromeHeight = 6
	tuiSidebarWidth = 28
	tuiSidebarGap   = 2
)

type tuiStyles struct {
	brand   lipgloss.Style
	accent  lipgloss.Style
	muted   lipgloss.Style
	success lipgloss.Style
	warning lipgloss.Style
	danger  lipgloss.Style
}

func newTUIStyles() tuiStyles {
	cyan := lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	green := lipgloss.AdaptiveColor{Light: "22", Dark: "42"}
	amber := lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	red := lipgloss.AdaptiveColor{Light: "124", Dark: "203"}
	gray := lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	return tuiStyles{
		brand:   lipgloss.NewStyle().Foreground(cyan).Bold(true),
		accent:  lipgloss.NewStyle().Foreground(cyan),
		muted:   lipgloss.NewStyle().Foreground(gray),
		success: lipgloss.NewStyle().Foreground(green),
		warning: lipgloss.NewStyle().Foreground(amber),
		danger:  lipgloss.NewStyle().Foreground(red).Bold(true),
	}
}

func (t *tui) resize(width, height int) {
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 30
	}
	t.width, t.height = width, height
	t.viewport.Width = t.transcriptWidth()
	t.viewport.Height = t.bodyHeight()
	t.input.Width = max(8, width-4)
	t.refreshTranscript(false)
}

func (t *tui) bodyHeight() int {
	return max(1, t.height-tuiChromeHeight)
}

func (t *tui) showSidebar() bool {
	return t.width >= 96
}

func (t *tui) transcriptWidth() int {
	if t.showSidebar() {
		return max(1, t.width-tuiSidebarWidth-tuiSidebarGap)
	}
	return max(1, t.width)
}

func (t *tui) refreshTranscript(forceBottom bool) {
	atBottom := t.viewport.AtBottom()
	t.viewport.SetContent(t.transcriptView(t.viewport.Width))
	if forceBottom || atBottom {
		t.viewport.GotoBottom()
	}
}

func (t *tui) transcriptView(width int) string {
	if width <= 0 {
		return ""
	}
	const labelWidth = 7
	contentWidth := max(1, width-labelWidth)
	var b strings.Builder
	for _, entry := range t.entries {
		label, style := "SYS", t.styles.muted
		switch entry.role {
		case tuiRoleYou:
			label, style = "YOU", t.styles.accent
		case tuiRoleAegis:
			label, style = "AEGIS", t.styles.success
		case tuiRoleError:
			label, style = "ERR", t.styles.danger
		}
		content := lipgloss.NewStyle().Width(contentWidth).Render(sanitizeTUIText(entry.text))
		for i, line := range strings.Split(content, "\n") {
			if i == 0 {
				b.WriteString(style.Render(fmt.Sprintf("%-*s", labelWidth-1, label)) + " ")
			} else {
				b.WriteString(strings.Repeat(" ", labelWidth))
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n\n")
}

func (t *tui) View() string {
	if t.width < 48 || t.height < 12 {
		return t.smallView()
	}
	header := t.headerView()
	divider := t.styles.muted.Render(strings.Repeat("─", t.width))
	body := t.viewport.View()
	if t.showSidebar() {
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, strings.Repeat(" ", tuiSidebarGap), t.sidebarView())
	}
	inputDivider := t.styles.muted.Render(strings.Repeat("─", t.width))
	return lipgloss.JoinVertical(lipgloss.Left, header, divider, body, inputDivider, t.input.View(), t.footerView())
}

func (t *tui) headerView() string {
	left := t.styles.brand.Render("AEGIS")
	if t.width >= 70 {
		left += t.styles.muted.Render("  SAFE OPERATIONS")
	}
	right := t.stateView()
	available := max(1, t.width-ansi.StringWidth(right)-1)
	left = ansi.Truncate(left, available, "…")
	gap := max(1, t.width-ansi.StringWidth(left)-ansi.StringWidth(right))
	context := fmt.Sprintf("ns:%s  agent:%s  session:%s", tuiNamespace(t.namespace), shortEndpoint(t.endpoint), sessionID(t))
	return left + strings.Repeat(" ", gap) + right + "\n" +
		t.styles.muted.Render(ansi.Truncate(context, t.width, "…"))
}

func (t *tui) stateView() string {
	switch {
	case t.busy():
		return t.styles.warning.Render(t.spinner.View() + " " + strings.ToUpper(t.status))
	case t.pendingChange != nil:
		return t.styles.warning.Render("CONFIRM")
	case t.status == "error":
		return t.styles.danger.Render("ERROR")
	default:
		return t.styles.success.Render("IDLE")
	}
}

func (t *tui) statusText() string {
	if t.busy() {
		return t.status
	}
	if t.pendingChange != nil {
		return "confirm"
	}
	return t.status
}

func (t *tui) sidebarView() string {
	heading := func(text string) string { return t.styles.brand.Render(text) }
	content := strings.Join([]string{
		heading("CONTEXT"),
		"ns      " + tuiNamespace(t.namespace),
		"session " + sessionID(t),
		"",
		heading("AGENT"),
		shortEndpoint(t.endpoint),
		"state   " + t.statusText(),
		"",
		heading("COMMANDS"),
		"/scan [ns]",
		"/status  /help",
		"/sessions /resume",
		"/change  /confirm",
		"/cancel  /exit",
		"",
		heading("SAFETY"),
		"read-only chat",
		"writes: gatekeeper",
		"R2: human approval",
	}, "\n")
	rendered := lipgloss.NewStyle().Width(tuiSidebarWidth).Render(content)
	lines := strings.Split(rendered, "\n")
	height := t.bodyHeight()
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (t *tui) footerView() string {
	text := "enter send  ·  ↑/↓ scroll  ·  esc cancel  ·  ctrl+c quit"
	switch {
	case t.busy():
		text = "operation running  ·  esc cancel  ·  ctrl+c quit"
	case t.pendingChange != nil:
		text = "review required  ·  /confirm submit or retry  ·  esc discard"
	case t.width < 80:
		text = "enter send · esc cancel · ctrl+c quit"
	}
	return t.styles.muted.Render(ansi.Truncate(text, t.width, "…"))
}

func (t *tui) smallView() string {
	message := t.styles.brand.Render("AEGIS") + "\n\n" +
		t.styles.warning.Render(fmt.Sprintf("Terminal too small: %dx%d\nNeed at least 48x12", t.width, t.height))
	message = lipgloss.NewStyle().Width(max(1, t.width-2)).Render(message)
	lines := strings.Split(message, "\n")
	if len(lines) > t.height {
		lines = lines[:t.height]
	}
	return lipgloss.Place(t.width, t.height, lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"))
}

func (t *tui) dump() error {
	path := strings.TrimSpace(os.Getenv("AEGIS_TUI_DUMP"))
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(ansi.Strip(t.View())), 0o600); err != nil { //nolint:gosec // 显式调试截图路径
		return fmt.Errorf("write tui dump: %w", err)
	}
	return nil
}

func sanitizeTUIText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range ansi.Strip(s) {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func tuiNamespace(ns string) string {
	if ns == "" {
		return "ALL"
	}
	return ns
}

func shortEndpoint(endpoint string) string {
	if u, err := url.Parse(strings.TrimSpace(endpoint)); err == nil && u.Host != "" {
		value := u.Host
		if u.Path != "" && u.Path != "/" {
			value += u.Path
		}
		return ansi.Truncate(value, 28, "…")
	}
	return ansi.Truncate(strings.TrimSpace(endpoint), 28, "…")
}
