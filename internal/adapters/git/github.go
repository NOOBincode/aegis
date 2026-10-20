// Package git 是 GitOps PR 通道的 git 托管平台适配器（M2 T2.10）。
//
// 只做 PR 创建这一条出站调用；分支/文件变更由调用方在 git 平台准备（或后续扩 adapter）。
// 不持有集群凭证，不能在集群上产生变更；token 只从调用方注入，不落日志。
package git

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

const maxBody = 2 << 20

// Config 是 GitHub 兼容 API 配置。
type Config struct {
	BaseURL string // 默认 https://api.github.com
	Token   string // 可为空（公开只读/测试）
	Owner   string
	Repo    string
	Timeout time.Duration
}

// Client 是 PR 创建客户端。
type Client struct {
	base  *url.URL
	token string
	owner string
	repo  string
	http  *http.Client
}

// New 构造客户端；owner/repo 必填。
func New(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = "https://api.github.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("非法 git API URL %q", base)
	}
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, fmt.Errorf("owner/repo 必填")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Client{base: u, token: cfg.Token, owner: cfg.Owner, repo: cfg.Repo, http: &http.Client{Timeout: cfg.Timeout}}, nil
}

// PullRequest 是创建 PR 的输入。
type PullRequest struct {
	Title string
	Head  string
	Base  string // 默认 main
	Body  string
}

// CreatePullRequest 创建 PR 并返回 html_url；失败返回错误，不返回半成功链接。
func (c *Client) CreatePullRequest(ctx context.Context, pr PullRequest) (string, error) {
	if strings.TrimSpace(pr.Head) == "" || strings.TrimSpace(pr.Title) == "" {
		return "", fmt.Errorf("PR head/title 必填")
	}
	if pr.Base == "" {
		pr.Base = "main"
	}
	payload, err := json.Marshal(map[string]string{"title": pr.Title, "head": pr.Head, "base": pr.Base, "body": pr.Body})
	if err != nil {
		return "", err
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/repos/" + c.owner + "/" + c.repo + "/pulls"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("创建 PR 请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("创建 PR HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析 PR 响应: %w", err)
	}
	if out.HTMLURL == "" {
		return "", fmt.Errorf("PR 响应缺 html_url")
	}
	return out.HTMLURL, nil
}
