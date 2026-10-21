package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// gatewayClient 是 TUI 到 gatekeeper MCP 的最小 Streamable HTTP 客户端。
// 它只调用 gatekeeper 的 request_change，不直接访问 kube-apiserver 写接口。
type gatewayClient struct {
	endpoint string
	session  string
	http     *http.Client
}

func newGatewayClient(endpoint string, timeout time.Duration) (*gatewayClient, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("gatekeeper endpoint 必须为 http(s) URL")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/mcp"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &gatewayClient{endpoint: u.String(), http: &http.Client{Timeout: timeout}}, nil
}

func (g *gatewayClient) initialize(ctx context.Context) error {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"aegis-cli-tui","version":"0.1"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := g.http.Do(req) //nolint:gosec // TUI 显式 gateway endpoint
	if err != nil {
		return fmt.Errorf("gatekeeper initialize: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("gatekeeper initialize HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	g.session = resp.Header.Get("Mcp-Session-Id")
	if g.session == "" {
		return fmt.Errorf("gatekeeper initialize 缺 Mcp-Session-Id")
	}
	n := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(n))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", g.session)
	if resp, err := g.http.Do(req); err == nil { //nolint:gosec // 初始化通知无返回语义
		_ = resp.Body.Close()
	}
	return nil
}

func (g *gatewayClient) call(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if g.session == "" {
		if err := g.initialize(ctx); err != nil {
			return nil, err
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", g.session)
	resp, err := g.http.Do(req) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("gatekeeper call %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gatekeeper call HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var rpc struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, fmt.Errorf("解析 gatekeeper 响应: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("gatekeeper error: %s", rpc.Error.Message)
	}
	if rpc.Result == nil || len(rpc.Result.Content) == 0 {
		return nil, fmt.Errorf("gatekeeper 响应缺 content")
	}
	text := rpc.Result.Content[0].Text
	var env struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return nil, fmt.Errorf("解析 gatekeeper data: %w", err)
	}
	data := strings.TrimSpace(env.Data)
	data = strings.TrimPrefix(data, "<untrusted_cluster_data>")
	data = strings.TrimSuffix(data, "</untrusted_cluster_data>")
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &out); err != nil {
		return nil, fmt.Errorf("解析 gatekeeper payload: %w", err)
	}
	return out, nil
}
