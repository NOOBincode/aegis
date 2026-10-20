// Package opa 是 OPA/Rego 适配器：策略求值的唯一实现位置（policies/ 为 bundle 源）。
//
// 边界：只调用 OPA HTTP API，不做策略语义推断；超时/错误/非 2xx 返回 fail-closed 的
// 错误（调用方必须拒绝写），不把“引擎不可用”解释为允许。策略输入由 policy 上下文提供。
package opa

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

const maxBodyBytes = 2 << 20

// Decision 是 OPA 求值结果；Allowed=false 时 Reason 必填。
type Decision struct {
	Allowed bool
	Reason  string
}

// Client 是只读 OPA 数据 API 客户端。
type Client struct {
	base *url.URL
	http *http.Client
}

// New 构造 OPA 客户端；baseURL 形如 http://opa:8181。
func New(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("非法 OPA URL %q", baseURL)
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Client{base: u, http: &http.Client{Timeout: timeout}}, nil
}

// Evaluate 调用 /v1/data/<path>；input 由调用方构造。result 支持 bool 或 {allow,reason}。
func (c *Client) Evaluate(ctx context.Context, path string, input any) (Decision, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return Decision{}, fmt.Errorf("OPA data path 不能为空")
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/v1/data/" + path
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return Decision{}, fmt.Errorf("编码 OPA input: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return Decision{}, fmt.Errorf("构造 OPA 请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("OPA 请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Decision{}, fmt.Errorf("读取 OPA 响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Decision{}, fmt.Errorf("OPA HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return Decision{}, fmt.Errorf("解析 OPA 响应: %w", err)
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return Decision{}, fmt.Errorf("OPA result 为空")
	}
	var allowed bool
	if err := json.Unmarshal(env.Result, &allowed); err == nil {
		if !allowed {
			return Decision{Allowed: false, Reason: "OPA 策略拒绝"}, nil
		}
		return Decision{Allowed: true}, nil
	}
	var obj struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(env.Result, &obj); err != nil {
		return Decision{}, fmt.Errorf("OPA result 既不是 bool 也不是 {allow,reason}: %w", err)
	}
	if !obj.Allow && obj.Reason == "" {
		obj.Reason = "OPA 策略拒绝"
	}
	return Decision{Allowed: obj.Allow, Reason: obj.Reason}, nil
}
