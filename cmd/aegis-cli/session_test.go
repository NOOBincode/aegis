package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRootInteractiveSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"artifacts":[{"parts":[{"kind":"text","text":"session reply"}]}]}}`))
	}))
	defer srv.Close()
	t.Setenv("AEGIS_AGENT_ENDPOINT", srv.URL)
	cmd := newRootCmd()
	cmd.SetArgs(nil)
	cmd.SetIn(strings.NewReader("hello\n/exit\n"))
	var out strings.Builder
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "session reply") || !strings.Contains(out.String(), "aegis>") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestRootHelpDoesNotEnterSession(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"--help"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Available Commands") {
		t.Fatalf("help out=%q", out.String())
	}
}

func TestIsScanLine(t *testing.T) {
	for _, s := range []string{"scan", "scan aegis-eval", "SCAN -n x"} {
		if !isScanLine(s) {
			t.Errorf("%q should be scan", s)
		}
	}
	for _, s := range []string{"scanner", "please scan"} {
		if isScanLine(s) {
			t.Errorf("%q should not be scan", s)
		}
	}
}
