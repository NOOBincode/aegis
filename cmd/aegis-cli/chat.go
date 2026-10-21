package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// chat 是面向 kagent A2A 的交互式诊断入口：只读对话，不直接执行写操作。
// 写操作仍必须经 gatekeeper 的 request_change。
func newChatCmd() *cobra.Command {
	var (
		endpoint string
		timeout  time.Duration
		message  string
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "与 kagent Agent 对话（A2A，只读诊断；写操作仍需 request_change）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if endpoint == "" {
				endpoint = os.Getenv("AEGIS_AGENT_ENDPOINT")
			}
			if endpoint == "" {
				endpoint = "http://127.0.0.1:18082/"
			}
			u, err := url.Parse(endpoint)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("endpoint 必须为 http(s) URL")
			}
			if message != "" {
				out, err := sendA2AMessage(cmd.Context(), endpoint, timeout, message)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), out)
				return err
			}
			return chatLoop(cmd, endpoint, timeout)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "kagent A2A endpoint（或用 AEGIS_AGENT_ENDPOINT；默认 http://127.0.0.1:18082/）")
	cmd.Flags().DurationVar(&timeout, "timeout", 240*time.Second, "单次对话超时")
	cmd.Flags().StringVar(&message, "message", "", "一次性发送消息后退出（非交互）")
	return cmd
}

func chatLoop(cmd *cobra.Command, endpoint string, timeout time.Duration) error {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "aegis chat（kagent A2A，只读诊断）。输入 /exit 退出，/help 查看帮助。")
	sc := bufio.NewScanner(cmd.InOrStdin())
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for {
		_, _ = fmt.Fprint(out, "aegis> ")
		if !sc.Scan() {
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch line {
		case "/exit", "/quit", "exit", "quit":
			return nil
		case "/help":
			_, _ = fmt.Fprintln(out, "/exit 退出；其他输入发送给 Agent。写操作请走 request_change。")
			continue
		}
		text, err := sendA2AMessage(cmd.Context(), endpoint, timeout, line)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "对话失败：", err)
			continue
		}
		_, _ = fmt.Fprintln(out, text)
	}
}

type a2aPart struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type a2aMessage struct {
	Role   string    `json:"role"`
	Parts  []a2aPart `json:"parts"`
	ID     string    `json:"messageId"`
	TaskID string    `json:"taskId,omitempty"`
}

type a2aResult struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Artifacts []struct {
		Parts []a2aPart `json:"parts"`
	} `json:"artifacts"`
	Status *struct {
		Message *a2aMessage `json:"message"`
	} `json:"status"`
}

func sendA2AMessage(ctx context.Context, endpoint string, timeout time.Duration, text string) (string, error) {
	if timeout <= 0 {
		timeout = 240 * time.Second
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "message/send",
		"params": map[string]any{"message": a2aMessage{
			Role: "user", Parts: []a2aPart{{Kind: "text", Text: text}}, ID: randomID(),
		}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body)) //nolint:gosec // CLI 显式 endpoint
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req) //nolint:gosec // CLI 显式 endpoint
	if err != nil {
		return "", fmt.Errorf("请求 Agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("agent HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var rpc struct {
		Result *a2aResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return "", fmt.Errorf("解析 A2A 响应: %w", err)
	}
	if rpc.Error != nil {
		return "", fmt.Errorf("A2A error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil {
		return "", fmt.Errorf("A2A 响应缺 result")
	}
	for _, a := range rpc.Result.Artifacts {
		var b strings.Builder
		for _, p := range a.Parts {
			if p.Kind == "text" && p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 {
			return b.String(), nil
		}
	}
	if rpc.Result.Status != nil && rpc.Result.Status.Message != nil {
		var b strings.Builder
		for _, p := range rpc.Result.Status.Message.Parts {
			if p.Kind == "text" && p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 {
			return b.String(), nil
		}
	}
	return "", fmt.Errorf("A2A 响应没有文本结论")
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
