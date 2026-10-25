package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseScaleIntent(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedNS   string
		expectedName string
		expectedFrom int
		expectedTo   int
		expectedOK   bool
	}{
		{
			name: "chinese scale up", input: "请把 aegis-blackbox/payments 从 2 个副本扩到 4 个副本",
			expectedNS: "aegis-blackbox", expectedName: "payments", expectedFrom: 2, expectedTo: 4, expectedOK: true,
		},
		{
			name: "english scale up", input: "scale aegis-blackbox/payments from 2 to 4",
			expectedNS: "aegis-blackbox", expectedName: "payments", expectedFrom: 2, expectedTo: 4, expectedOK: true,
		},
		{
			name: "short chinese scale up", input: "把 shop/api 从 1 扩到 3",
			expectedNS: "shop", expectedName: "api", expectedFrom: 1, expectedTo: 3, expectedOK: true,
		},
		{name: "scan is not scale", input: "scan kagent"},
		{name: "unrelated text", input: "help me"},
		{name: "unchanged replicas", input: "aegis-blackbox/payments 从 4 扩到 4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns, name, from, to, ok := parseScaleIntent(tt.input)
			if ok != tt.expectedOK || ns != tt.expectedNS || name != tt.expectedName || from != tt.expectedFrom || to != tt.expectedTo {
				t.Errorf("%q => %q/%q %d->%d ok=%v", tt.input, ns, name, from, to, ok)
			}
		})
	}
}

func TestTUIScaleIntentRequiresConfirmation(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			calls.Add(1)
			inner, err := json.Marshal(map[string]string{
				"data": `<untrusted_cluster_data>{"cr_name":"cr-1","created":true}</untrusted_cluster_data>`,
			})
			if err != nil {
				http.Error(w, "encode error", http.StatusInternalServerError)
				return
			}
			resp := map[string]any{
				"jsonrpc": "2.0",
				"result": map[string]any{
					"content": []map[string]any{{"text": string(inner)}},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	ui, _ := newTestTUI(t, strings.NewReader(""))
	ui.gateway = srv.URL
	if _, err := ui.handle("scale shop/api from 1 to 3"); err != nil {
		t.Fatal(err)
	}
	if ui.pendingChange == nil || calls.Load() != 0 {
		t.Fatalf("pending=%v calls=%d", ui.pendingChange, calls.Load())
	}
	if !strings.Contains(ui.entries[len(ui.entries)-1].text, "No request sent") {
		t.Fatalf("entry=%+v", ui.entries[len(ui.entries)-1])
	}
	if _, err := ui.handle("/cancel"); err != nil {
		t.Fatal(err)
	}
	if ui.pendingChange != nil || calls.Load() != 0 {
		t.Fatalf("cancel sent a request: pending=%v calls=%d", ui.pendingChange, calls.Load())
	}

	if _, err := ui.handle("请解释为什么 shop/api 从 1 扩到 3"); err != nil {
		t.Fatal(err)
	}
	cmd, err := ui.handle("/confirm")
	if err != nil {
		t.Fatal(err)
	}
	runCmdForTest(ui, cmd)
	if ui.pendingChange != nil || calls.Load() != 1 {
		t.Fatalf("confirm calls=%d pending=%v", calls.Load(), ui.pendingChange)
	}
	if _, err := ui.handle("/confirm"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("repeated confirm submitted again: calls=%d", calls.Load())
	}
}

func TestTUIChangeRetryKeepsIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	keys := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Arguments struct {
					IdempotencyKey string `json:"idempotency_key"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			calls.Add(1)
			keys <- req.Params.Arguments.IdempotencyKey
			if calls.Load() == 1 {
				http.Error(w, "lost response", http.StatusInternalServerError)
				return
			}
			writeGatewaySuccess(w)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	ui, _ := newTestTUI(t, strings.NewReader(""))
	ui.gateway = srv.URL
	if _, err := ui.handle("scale shop/api from 1 to 3"); err != nil {
		t.Fatal(err)
	}
	cmd, err := ui.handle("/confirm")
	if err != nil {
		t.Fatal(err)
	}
	runCmdForTest(ui, cmd)
	if ui.pendingChange == nil {
		t.Fatal("failed change was cleared")
	}
	firstKey := ui.pendingChange.idempotencyKey
	cmd, err = ui.handle("/confirm")
	if err != nil {
		t.Fatal(err)
	}
	runCmdForTest(ui, cmd)
	if ui.pendingChange != nil {
		t.Fatal("successful retry remained pending")
	}
	firstSent, secondSent := <-keys, <-keys
	if firstSent == "" || firstSent != firstKey || secondSent != firstKey {
		t.Fatalf("idempotency keys changed: first=%q second=%q expected=%q", firstSent, secondSent, firstKey)
	}
}

func TestTUIActionCanBeCancelled(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			close(started)
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
				http.Error(w, "request was not cancelled", http.StatusGatewayTimeout)
			}
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	ui, _ := newTestTUI(t, strings.NewReader(""))
	ui.gateway = srv.URL
	if _, err := ui.handle("scale shop/api from 1 to 3"); err != nil {
		t.Fatal(err)
	}
	cmd, err := ui.handle("/confirm")
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan tea.Msg, 1)
	go func() { messages <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("change did not start")
	}
	expectedKey := ui.pendingChange.idempotencyKey
	submitForTest(ui, "/cancel")
	select {
	case msg := <-messages:
		ui.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled action did not return")
	}
	if ui.actionRunning || ui.pendingChange == nil || ui.pendingChange.idempotencyKey != expectedKey {
		t.Fatalf("cancel lost retry state: %+v", ui.pendingChange)
	}
	if !strings.Contains(entriesText(ui.entries), "operation cancelled") {
		t.Fatalf("entries=%s", entriesText(ui.entries))
	}
	submitForTest(ui, "/cancel")
	got := entriesText(ui.entries)
	if !strings.Contains(got, "a request may have been sent") || strings.Contains(got, "pending change discarded; no request sent") {
		t.Fatalf("discard misreports an attempted change: %s", got)
	}
}

func writeGatewaySuccess(w http.ResponseWriter) {
	inner, err := json.Marshal(map[string]string{
		"data": `<untrusted_cluster_data>{"cr_name":"cr-1","created":true}</untrusted_cluster_data>`,
	})
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"result": map[string]any{
			"content": []map[string]any{{"text": string(inner)}},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
