// Package git 是 GitOps PR 通道的 git 托管平台适配器（M2 T2.10）。
//
// 只做 PR 创建这一条出站调用；分支/文件变更由调用方在 git 平台准备（或后续扩 adapter）。
// 不持有集群凭证，不能在集群上产生变更；token 只从调用方注入，不落日志。
package git

import (
	"bytes"
	"context"
	"encoding/base64"
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

// EnsureBranch 从 from 分支创建 branch；已存在视为幂等成功。
func (c *Client) EnsureBranch(ctx context.Context, branch, from string) error {
	if strings.TrimSpace(branch) == "" || strings.TrimSpace(from) == "" {
		return fmt.Errorf("branch/from 必填")
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/repos/" + c.owner + "/" + c.repo + "/git/ref/heads/" + escapePath(from)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("读取 base ref: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("读取 base ref HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil || ref.Object.SHA == "" {
		return fmt.Errorf("base ref 缺 sha")
	}
	body, _ := json.Marshal(map[string]string{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA})
	u.Path = strings.TrimRight(c.base.Path, "/") + "/repos/" + c.owner + "/" + c.repo + "/git/refs"
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err = c.http.Do(req)
	if err != nil {
		return fmt.Errorf("创建 branch: %w", err)
	}
	raw, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnprocessableEntity && strings.Contains(string(raw), "already exists") {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("创建 branch HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// PutFile 在 branch 上创建/更新文件；先读 sha，再 PUT base64 内容。
func (c *Client) PutFile(ctx context.Context, branch, filePath string, content []byte, message string) error {
	if strings.TrimSpace(branch) == "" || strings.TrimSpace(filePath) == "" || len(content) == 0 {
		return fmt.Errorf("branch/path/content 必填")
	}
	if err := validateRepoPath(filePath); err != nil {
		return err
	}
	contentPath := escapePath(filePath)
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/repos/" + c.owner + "/" + c.repo + "/contents/" + contentPath
	q := u.Query()
	q.Set("ref", branch)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("读取文件 sha: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
	var sha string
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// 新建
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var file struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(raw, &file); err != nil || file.SHA == "" {
			return fmt.Errorf("文件响应缺 sha")
		}
		sha = file.SHA
	default:
		return fmt.Errorf("读取文件 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	payload := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
	}
	if sha != "" {
		payload["sha"] = sha
	}
	body, _ := json.Marshal(payload)
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err = c.http.Do(req)
	if err != nil {
		return fmt.Errorf("提交文件: %w", err)
	}
	raw, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("提交文件 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func escapePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func validateRepoPath(p string) error {
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("非法仓库路径")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("非法仓库路径")
		}
	}
	return nil
}
