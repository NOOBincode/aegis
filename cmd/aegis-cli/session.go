package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// session 是 aegis-cli 的默认交互入口：不带子命令时直接进入。
// 它把本地确定性 scan 与 kagent A2A 对话放在同一个 prompt 里；
// 写操作仍只经 gatekeeper request_change，不在本会话直接执行。
type interactiveSession struct {
	cmd        *cobra.Command
	out        io.Writer
	errOut     io.Writer
	endpoint   string
	namespace  string
	kubeconfig string
	timeout    time.Duration
}

func newSession(cmd *cobra.Command) *interactiveSession {
	endpoint := strings.TrimSpace(os.Getenv("AEGIS_AGENT_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:18082/"
	}
	ns := strings.TrimSpace(os.Getenv("AEGIS_NAMESPACE"))
	kc := strings.TrimSpace(os.Getenv("AEGIS_KUBECONFIG"))
	if kc == "" {
		kc = defaultKubeconfig()
	}
	return &interactiveSession{
		cmd: cmd, out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(),
		endpoint: endpoint, namespace: ns, kubeconfig: kc, timeout: 240 * time.Second,
	}
}

func (s *interactiveSession) run() error {
	_, _ = fmt.Fprintln(s.out, "aegis 交互式安全运维会话（只读对话 + 本地确定性 scan）")
	_, _ = fmt.Fprintln(s.out, "输入自然语言对话；/scan 在本地跑 Tier-0；/help 查看命令；/exit 退出。")
	_, _ = fmt.Fprintf(s.out, "agent: %s\nnamespace: %s\n\n", s.endpoint, displayNamespace(s.namespace))
	sc := bufio.NewScanner(s.cmd.InOrStdin())
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for {
		_, _ = fmt.Fprint(s.out, "aegis> ")
		if !sc.Scan() {
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := s.handle(line); err != nil {
			if errors.Is(err, errSessionExit) {
				return nil
			}
			_, _ = fmt.Fprintln(s.errOut, err)
		}
	}
}

func (s *interactiveSession) handle(line string) error {
	if strings.HasPrefix(line, "/") {
		return s.slash(line)
	}
	if isScanLine(line) {
		return s.scan(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "scan")))
	}
	_, _ = fmt.Fprintln(s.out, "\n[agent] 调用中...")
	text, err := sendA2AMessage(s.cmd.Context(), s.endpoint, s.timeout, line)
	if err != nil {
		return fmt.Errorf("agent 未连接或调用失败：%v\n可先执行 /scan 在本地巡检，或用 /agent <url> 设置端口转发地址", err)
	}
	_, _ = fmt.Fprintln(s.out, text)
	_, _ = fmt.Fprintln(s.out)
	return nil
}

func (s *interactiveSession) slash(line string) error {
	fields := strings.Fields(line)
	switch fields[0] {
	case "/exit", "/quit":
		return errSessionExit
	case "/help":
		s.printHelp()
		return nil
	case "/status":
		_, _ = fmt.Fprintf(s.out, "agent=%s\nnamespace=%s\nkubeconfig=%s\ntimeout=%s\n", s.endpoint, displayNamespace(s.namespace), s.kubeconfig, s.timeout)
		return nil
	case "/agent":
		if len(fields) < 2 {
			_, _ = fmt.Fprintln(s.out, "用法：/agent <http(s)://host:port/>")
			return nil
		}
		u, err := url.Parse(fields[1])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("agent endpoint 必须是 http(s) URL")
		}
		s.endpoint = fields[1]
		_, _ = fmt.Fprintf(s.out, "agent endpoint = %s\n", s.endpoint)
		return nil
	case "/namespace":
		if len(fields) < 2 {
			s.namespace = ""
			_, _ = fmt.Fprintln(s.out, "namespace = 全部")
			return nil
		}
		s.namespace = fields[1]
		_, _ = fmt.Fprintf(s.out, "namespace = %s\n", s.namespace)
		return nil
	case "/scan":
		return s.scan(strings.Join(fields[1:], " "))
	default:
		return fmt.Errorf("未知命令 %s；/help 查看", fields[0])
	}
}

func (s *interactiveSession) scan(args string) error {
	// /scan [namespace|json|text|sarif]；默认继承会话 namespace。
	ns := s.namespace
	format := ""
	fields := strings.Fields(args)
	for i := 0; i < len(fields); i++ {
		a := fields[i]
		switch {
		case (a == "-n" || a == "--namespace") && i+1 < len(fields):
			ns = fields[i+1]
			i++
		case strings.HasPrefix(a, "--namespace="):
			ns = strings.TrimPrefix(a, "--namespace=")
		case a == "json" || a == "text" || a == "sarif":
			format = a
		case !strings.HasPrefix(a, "-"):
			ns = a
		}
	}
	cmdArgs := []string{"--kubeconfig", s.kubeconfig}
	if ns != "" {
		cmdArgs = append(cmdArgs, "--namespace", ns)
	}
	if format != "" {
		cmdArgs = append(cmdArgs, "--format", format)
	}
	scanCmd := newScanCmd()
	scanCmd.SetContext(s.cmd.Context())
	scanCmd.SetOut(s.out)
	scanCmd.SetErr(s.errOut)
	scanCmd.SetArgs(cmdArgs)
	return scanCmd.Execute()
}

func (s *interactiveSession) printHelp() {
	_, _ = fmt.Fprintln(s.out, `可用输入：
  自然语言            发给 kagent Agent（需要 agent endpoint + LLM key）
  /scan [ns] [json]   本地 Tier-0 确定性巡检（json/text/sarif）
  /namespace <ns>     设置后续 /scan 默认命名空间
  /agent <url>        设置 kagent A2A endpoint
  /status             显示当前 endpoint/namespace/kubeconfig
  /help               本帮助
  /exit               退出

安全边界：会话只读；写操作请使用 gatekeeper request_change + approve。`)
}

func isScanLine(line string) bool {
	l := strings.ToLower(strings.TrimSpace(line))
	return l == "scan" || strings.HasPrefix(l, "scan ")
}

func displayNamespace(ns string) string {
	if ns == "" {
		return "全部"
	}
	return ns
}

var errSessionExit = fmt.Errorf("session exit")
