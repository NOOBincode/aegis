package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestChangeRejectsAdditionalPropertiesInSchema(t *testing.T) {
	srv := newGatekeeperMCPServer(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler, err := srv.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()
	client := httpSrv.Client()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	headers, _ := postMCP(ctx, t, client, httpSrv.URL, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"contract-test","version":"0"}}}`)
	sessionID := headers.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize response has no MCP session")
	}
	postMCP(ctx, t, client, httpSrv.URL, sessionID, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	_, response := postMCP(ctx, t, client, httpSrv.URL, sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)

	var result struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					AdditionalProperties *bool `json:"additionalProperties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(result.Error) > 0 {
		t.Fatalf("tools/list returned error: %s", result.Error)
	}
	if len(result.Result.Tools) != 1 || result.Result.Tools[0].Name != "request_change" {
		t.Fatalf("unexpected tools: %+v", result.Result.Tools)
	}
	additional := result.Result.Tools[0].InputSchema.AdditionalProperties
	if additional == nil || *additional {
		t.Fatalf("request_change must reject additional properties: %+v", result.Result.Tools[0].InputSchema)
	}
}

func postMCP(ctx context.Context, t *testing.T, client *http.Client, endpoint, sessionID, body string) (http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("MCP HTTP %d: %s", resp.StatusCode, data)
	}
	if len(data) > 0 && !json.Valid(data) {
		t.Fatalf("MCP response is not JSON: %s", data)
	}
	return resp.Header, data
}
