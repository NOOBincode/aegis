package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGatewayClientCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/mcp") {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			switch req["method"] {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", "sess-1")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/call":
				inner, _ := json.Marshal(map[string]any{"data": "<untrusted_cluster_data>{\"cr_name\":\"cr-1\",\"created\":true}</untrusted_cluster_data>"})
				resp := map[string]any{"jsonrpc": "2.0", "result": map[string]any{"content": []map[string]any{{"text": string(inner)}}}}
				_ = json.NewEncoder(w).Encode(resp)
			}
		}
	}))
	defer srv.Close()
	g, err := newGatewayClient(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	out, err := g.call(context.Background(), "request_change", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if out["cr_name"] != "cr-1" || out["created"] != true {
		t.Fatalf("out=%v", out)
	}
}
