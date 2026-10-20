package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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

	"github.com/aegis-dev/aegis/internal/session"
)

// approvalPayload 是 CLI 提交给 gatekeeper 审批接口的签名载荷。
// CLI 不做放行/拒绝判定；它只把“人审意图 + 操作者 + note”签名后提交，
// 审批状态机与身份绑定在服务端（T2.4/R16）。
type approvalPayload struct {
	CR      string `json:"cr"`
	Approve bool   `json:"approve"`
	Note    string `json:"note,omitempty"`
	Actor   string `json:"actor"`
}

type approvalClientTLS struct {
	CA, Cert, Key, ServerName string
}

func newApproveCmd(approve bool) *cobra.Command {
	verb := "approve"
	if !approve {
		verb = "deny"
	}
	var (
		endpoint string
		note     string
		actor    string
		crFlag   string
		timeout  time.Duration
		mtls     approvalClientTLS
	)
	cmd := &cobra.Command{
		Use:   verb + " [namespace/name]",
		Short: "提交审批通过/拒绝意图（服务端状态机；CLI 不含判定逻辑）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cr := crFlag
			if len(args) == 1 {
				if cr != "" && cr != args[0] {
					return fmt.Errorf("--cr 与位置参数冲突")
				}
				cr = args[0]
			}
			if cr == "" {
				return fmt.Errorf("必须提供 CR：位置参数或 --cr namespace/name")
			}
			return runApprovalWithTLS(cmd, cr, approve, endpoint, note, actor, timeout, mtls)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "gatekeeper 审批接口 URL（或用 AEGIS_APPROVAL_ENDPOINT）")
	cmd.Flags().StringVar(&crFlag, "cr", "", "ChangeRequest namespace/name")
	cmd.Flags().StringVar(&note, "note", "", "审批意见（可审计）")
	cmd.Flags().StringVar(&actor, "actor", "", "操作者身份（默认 AEGIS_ACTOR；生产应来自 mTLS 服务端身份）")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "HTTP 超时")
	cmd.Flags().StringVar(&mtls.CA, "tls-ca", "", "mTLS CA 文件")
	cmd.Flags().StringVar(&mtls.Cert, "tls-cert", "", "mTLS 客户端证书")
	cmd.Flags().StringVar(&mtls.Key, "tls-key", "", "mTLS 客户端私钥")
	cmd.Flags().StringVar(&mtls.ServerName, "tls-server-name", "", "TLS 校验用 server name")
	return cmd
}

func runApproval(cmd *cobra.Command, cr string, approve bool, endpoint, note, actor string, timeout time.Duration) error {
	return runApprovalWithTLS(cmd, cr, approve, endpoint, note, actor, timeout, approvalClientTLS{})
}

func runApprovalWithTLS(cmd *cobra.Command, cr string, approve bool, endpoint, note, actor string, timeout time.Duration, mtls approvalClientTLS) error {
	if !strings.Contains(cr, "/") {
		return fmt.Errorf("CR 必须为 namespace/name")
	}
	if endpoint == "" {
		endpoint = os.Getenv("AEGIS_APPROVAL_ENDPOINT")
	}
	if endpoint == "" {
		return fmt.Errorf("--endpoint 或 AEGIS_APPROVAL_ENDPOINT 必填")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("endpoint 必须为 http(s) URL")
	}
	if actor == "" {
		actor = os.Getenv("AEGIS_ACTOR")
	}
	if actor == "" {
		return fmt.Errorf("操作者身份为空：请经 --actor 或 AEGIS_ACTOR 注入（生产由服务端认证）")
	}
	secret, err := approvalSecret()
	if err != nil {
		return err
	}
	signer, err := session.NewSigner(secret, time.Minute)
	if err != nil {
		return err
	}
	sid := "cli-" + randHex(8)
	payload, _ := json.Marshal(approvalPayload{CR: cr, Approve: approve, Note: note, Actor: actor})
	token, err := signer.Seal(sid, 1, payload)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, endpoint, bytes.NewReader(body)) //nolint:gosec // CLI 显式用户指定审批端点
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client, err := approvalHTTPClient(endpoint, timeout, mtls)
	if err != nil {
		return err
	}
	resp, err := client.Do(req) //nolint:gosec // 同上：用户显式 endpoint；非服务端出站
	if err != nil {
		return fmt.Errorf("提交审批失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("审批接口 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		State    string `json:"state"`
		Approver string `json:"approver"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("解析审批响应: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "state=%s approver=%s message=%s\n", out.State, out.Approver, out.Message)
	return err
}

func approvalHTTPClient(endpoint string, timeout time.Duration, mtls approvalClientTLS) (*http.Client, error) {
	if mtls.CA == "" && mtls.Cert == "" && mtls.Key == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	if mtls.CA == "" || mtls.Cert == "" || mtls.Key == "" {
		return nil, fmt.Errorf("mTLS 需要同时提供 --tls-ca/--tls-cert/--tls-key")
	}
	if !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("使用 mTLS 时 endpoint 必须为 https://")
	}
	caPEM, err := os.ReadFile(mtls.CA) //nolint:gosec // CLI 显式证书路径
	if err != nil {
		return nil, fmt.Errorf("读取 CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA 无有效证书")
	}
	cert, err := tls.LoadX509KeyPair(mtls.Cert, mtls.Key)
	if err != nil {
		return nil, fmt.Errorf("加载客户端证书: %w", err)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: mtls.ServerName,
	}}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}

func approvalSecret() ([]byte, error) {
	s := os.Getenv("AEGIS_APPROVAL_SECRET")
	if s == "" {
		return nil, fmt.Errorf("AEGIS_APPROVAL_SECRET 未设置")
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil && len(b) >= 16 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) >= 16 {
		return b, nil
	}
	return nil, fmt.Errorf("AEGIS_APPROVAL_SECRET 必须是 base64 编码的 ≥16 字节密钥")
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b)
}
