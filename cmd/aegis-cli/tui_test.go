package main

import (
	"strings"
	"testing"
)

func TestTUIRenderLayout(t *testing.T) {
	t.Setenv("AEGIS_TUI_WIDTH", "100")
	t.Setenv("AEGIS_SESSION_DIR", t.TempDir())
	t.Setenv("AEGIS_TUI_HEIGHT", "30")
	cmd := newRootCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	ui := newTUI(cmd)
	ui.entries = append(ui.entries, tuiEntry{tuiRoleYou, "/scan kagent"})
	ui.entries = append(ui.entries, tuiEntry{tuiRoleAegis, "Scanned 14 pods | 28 findings"})
	ui.render()
	got := out.String()
	for _, want := range []string{"SECURITY OPERATIONS CONSOLE", "READ-ONLY", "QUICK START", "/scan [ns]", "YOU  |", "AEGIS|"} {
		if !strings.Contains(got, want) {
			t.Errorf("TUI missing %q", want)
		}
	}
}
