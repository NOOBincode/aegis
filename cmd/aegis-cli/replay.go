package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/audit"
)

// replay 子命令：T1.8 DecisionRecord 的离线回放（只读、零外部调用）。
// 输入两种：
//   - MCP JSONL 审计日志（kubectl logs 的 mcp.tool.invoke 行）→ 工具链；
//   - DecisionRecord YAML/JSON（CRD manifest）→ 完整记录。
//
// 输出 text 默认；yaml 便于把一次会话固化成 K8s 对象。cmd 只做装配/转换，
// 解析与渲染逻辑在 internal/audit（纯域）。
func newReplayCmd() *cobra.Command {
	var (
		logFile     string
		recordFile  string
		outcome     string
		outcomeFile string
		format      string
		output      string
	)
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "回放 DecisionRecord / MCP 审计日志（T1.8；离线、只读）",
		Long: "把一次诊断会话的 DecisionRecord（或 MCP 审计 JSONL）渲染成可审计的决策链：" +
			"工具序列、参数预览、结果哈希/预览、脱敏计数、结论。不调用 LLM、不写集群。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReplay(cmd, logFile, recordFile, outcome, outcomeFile, format, output)
		},
	}
	cmd.Flags().StringVar(&logFile, "log-file", "", "MCP 审计 JSONL 文件（- 表示 stdin）")
	cmd.Flags().StringVar(&recordFile, "record", "", "DecisionRecord YAML/JSON 文件")
	cmd.Flags().StringVar(&outcome, "outcome", "", "结论文本（与 audit log 组合时写入记录）")
	cmd.Flags().StringVar(&outcomeFile, "outcome-file", "", "结论文件（与 --outcome 互斥）")
	cmd.Flags().StringVarP(&format, "format", "f", "text", "输出格式：text|yaml")
	cmd.Flags().StringVarP(&output, "output", "o", "", "输出文件（空=stdout；写文件权限 0600）")
	return cmd
}

func runReplay(cmd *cobra.Command, logFile, recordFile, outcome, outcomeFile, format, output string) error {
	if (logFile == "") == (recordFile == "") {
		return fmt.Errorf("--log-file 与 --record 必须且只能指定一个")
	}
	if outcome != "" && outcomeFile != "" {
		return fmt.Errorf("--outcome 与 --outcome-file 互斥")
	}
	if outcomeFile != "" {
		b, err := os.ReadFile(outcomeFile) //nolint:gosec // CLI 显式本地输入；非网络参数
		if err != nil {
			return fmt.Errorf("读取 outcome 文件: %w", err)
		}
		outcome = string(b)
	}
	if format != "text" && format != "yaml" {
		return fmt.Errorf("--format 仅支持 text|yaml")
	}
	rec, err := loadRecord(logFile, recordFile, outcome)
	if err != nil {
		return err
	}
	w, closeFn, err := replayWriter(cmd.OutOrStdout(), output)
	if err != nil {
		return err
	}
	defer closeFn()
	if format == "yaml" {
		dr := apiRecordFromAudit(rec)
		b, err := yaml.Marshal(&dr)
		if err != nil {
			return fmt.Errorf("marshal decisionrecord: %w", err)
		}
		_, err = w.Write(b)
		return err
	}
	return audit.Render(w, rec)
}

func loadRecord(logFile, recordFile, outcome string) (audit.Record, error) {
	if recordFile != "" {
		b, err := os.ReadFile(recordFile) //nolint:gosec // CLI 显式本地输入；非网络参数
		if err != nil {
			return audit.Record{}, fmt.Errorf("读取 DecisionRecord: %w", err)
		}
		var dr aegisv1.DecisionRecord
		if err := yaml.Unmarshal(b, &dr); err != nil {
			return audit.Record{}, fmt.Errorf("解析 DecisionRecord: %w", err)
		}
		if dr.Spec.SessionRef == "" {
			return audit.Record{}, fmt.Errorf("DecisionRecord.spec.sessionRef 为空")
		}
		rec := auditRecordFromAPI(&dr)
		if outcome != "" {
			rec.Outcome = strings.TrimSpace(outcome)
		}
		return rec, nil
	}
	var r io.Reader = os.Stdin
	if logFile != "-" {
		f, err := os.Open(logFile) //nolint:gosec // CLI 显式本地输入；非网络参数
		if err != nil {
			return audit.Record{}, fmt.Errorf("打开审计日志: %w", err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	calls, err := audit.ParseLog(r)
	if err != nil {
		return audit.Record{}, err
	}
	if len(calls) == 0 {
		return audit.Record{}, fmt.Errorf("审计日志中没有 mcp.tool.invoke 记录（检查 kubectl logs 是否混入前缀/日志级别）")
	}
	rec := audit.BuildRecord(calls)
	rec.Outcome = strings.TrimSpace(outcome)
	return rec, nil
}

func replayWriter(stdout io.Writer, output string) (io.Writer, func(), error) {
	if output == "" || output == "-" {
		return stdout, func() {}, nil
	}
	// DecisionRecord 含集群数据预览；文件权限 0600，避免落到 group/world。
	f, err := os.OpenFile(output, //nolint:gosec // CLI 显式本地输出；非网络参数
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, func() {}, fmt.Errorf("打开输出文件: %w", err)
	}
	return f, func() { _ = f.Close() }, nil
}

func auditRecordFromAPI(dr *aegisv1.DecisionRecord) audit.Record {
	rec := audit.Record{
		SessionRef:         dr.Spec.SessionRef,
		ClusterSnapshotRef: dr.Spec.ClusterSnapshotRef,
		LLMTraceRef:        dr.Spec.LLMTraceRef,
		ChangeRequests:     dr.Spec.ChangeRequests,
		Outcome:            dr.Spec.Outcome,
		RetentionDays:      dr.Spec.RetentionDays,
	}
	for _, c := range dr.Spec.ToolCalls {
		tc := audit.ToolCall{
			Seq: c.Seq, SessionID: c.SessionID, Tool: c.Tool, ArgsPreview: c.ArgsPreview,
			ResultSHA256: c.ResultSHA256, ResultBytes: c.ResultBytes, ResultPreview: c.ResultPreview,
			ErrorClass: c.ErrorClass, RedactedCount: c.RedactedCount, LatencyMs: c.LatencyMs,
		}
		if t, err := time.Parse(time.RFC3339Nano, c.Timestamp); err == nil {
			tc.Timestamp = t
		}
		rec.ToolCalls = append(rec.ToolCalls, tc)
	}
	for _, e := range dr.Spec.SanitizationEvents {
		ev := audit.SanitizationEvent{Count: e.Count, Source: e.Source}
		if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			ev.Timestamp = t
		}
		rec.SanitizationEvents = append(rec.SanitizationEvents, ev)
	}
	return rec
}

func apiRecordFromAudit(rec audit.Record) aegisv1.DecisionRecord {
	dr := aegisv1.DecisionRecord{
		TypeMeta: metav1.TypeMeta{APIVersion: "aegis.dev/v1alpha1", Kind: "DecisionRecord"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "decisionrecord-" + sanitizeName(rec.SessionRef),
		},
		Spec: aegisv1.DecisionRecordSpec{
			SessionRef: rec.SessionRef, ClusterSnapshotRef: rec.ClusterSnapshotRef,
			LLMTraceRef: rec.LLMTraceRef, ChangeRequests: rec.ChangeRequests,
			Outcome: rec.Outcome, RetentionDays: rec.RetentionDays,
		},
	}
	for _, c := range rec.ToolCalls {
		ts := ""
		if !c.Timestamp.IsZero() {
			ts = c.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		dr.Spec.ToolCalls = append(dr.Spec.ToolCalls, aegisv1.ToolCall{
			Seq: c.Seq, Timestamp: ts, SessionID: c.SessionID, Tool: c.Tool,
			ArgsPreview: c.ArgsPreview, ResultSHA256: c.ResultSHA256, ResultBytes: c.ResultBytes,
			ResultPreview: c.ResultPreview, ErrorClass: c.ErrorClass, RedactedCount: c.RedactedCount,
			LatencyMs: c.LatencyMs,
		})
	}
	for _, e := range rec.SanitizationEvents {
		ts := ""
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		dr.Spec.SanitizationEvents = append(dr.Spec.SanitizationEvents, aegisv1.SanitizationEvent{
			Timestamp: ts, Count: e.Count, Source: e.Source,
		})
	}
	return dr
}

// sanitizeName 把 MCP session 集合转成合法 DNS label（仅用于输出记录名）。
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "unknown"
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}
