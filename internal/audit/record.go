package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ToolCall 是一次只读工具调用的审计快照（M1 T1.8）。字段与
// api/v1alpha1.DecisionRecord 的 spec.toolCalls 对齐；本包不 import k8s，
// 由 cmd/aegis-cli 在装配层做契约转换（DDD 依赖方向）。
type ToolCall struct {
	Seq               int
	Timestamp         time.Time
	SessionID         string
	Tool              string
	ArgsPreview       string
	ResultSHA256      string
	ResultBytes       int
	ResultPreview     string
	ErrorClass        string
	RedactedCount     int
	LatencyMs         int64
	InjectionHit      bool
	InjectionPatterns []string
}

// SanitizationEvent 是脱敏命中的聚合事件（只计数，不含原值）。
type SanitizationEvent struct {
	Timestamp time.Time
	Count     int
	Source    string
}

// Record 是回放用的决策链（与 CRD 的 spec 同构）。
type Record struct {
	SessionRef         string
	ClusterSnapshotRef string
	LLMTraceRef        string
	ToolCalls          []ToolCall
	SanitizationEvents []SanitizationEvent
	ChangeRequests     []string
	Outcome            string
	RetentionDays      int
}

// ParseLog 从 mcpserver 的 JSONL 审计日志中提取 mcp.tool.invoke 记录。
// 混合了其它 slog 行时忽略；坏行跳过而不是让整条链失败（日志可能被截断）。
func ParseLog(r io.Reader) ([]ToolCall, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var calls []ToolCall
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.Contains(line, `"mcp.tool.invoke"`) {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if msg, _ := m["msg"].(string); msg != "mcp.tool.invoke" {
			continue
		}
		tc := ToolCall{
			SessionID:         str(m["session_id"]),
			Tool:              str(m["tool_name"]),
			ArgsPreview:       str(m["args_preview"]),
			ResultSHA256:      str(m["result_sha256"]),
			ResultPreview:     str(m["result_preview"]),
			ErrorClass:        str(m["error_class"]),
			ResultBytes:       intOf(m["result_bytes"]),
			RedactedCount:     intOf(m["redacted_count"]),
			LatencyMs:         int64(intOf(m["latency_ms"])),
			InjectionHit:      boolOf(m["injection_hit"]),
			InjectionPatterns: stringsOf(m["injection_patterns"]),
		}
		if tc.Tool == "" {
			continue
		}
		if ts, ok := parseTime(m["time"]); ok {
			tc.Timestamp = ts
		}
		calls = append(calls, tc)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	sort.SliceStable(calls, func(i, j int) bool {
		if calls[i].Timestamp.Equal(calls[j].Timestamp) {
			return calls[i].Tool < calls[j].Tool
		}
		return calls[i].Timestamp.Before(calls[j].Timestamp)
	})
	for i := range calls {
		calls[i].Seq = i + 1
	}
	return calls, nil
}

// BuildRecord 把工具调用聚合为可回放的 Record；会话 ID 可能因每个 MCP server
// 独立 session 而不同，故 SessionRef 取去重后的集合（M1 以时间窗串联）。
func BuildRecord(calls []ToolCall) Record {
	rec := Record{}
	seen := map[string]bool{}
	for i := range calls {
		// 输入快照引用：优先用 scan_cluster 结果哈希；没有 scan 则退化为首个工具结果哈希。
		// 这样 replay 能指出"基于哪份输入快照"，而不是只给 session。
		if rec.ClusterSnapshotRef == "" && calls[i].ResultSHA256 != "" &&
			(calls[i].Tool == "scan_cluster" || i == 0) {
			rec.ClusterSnapshotRef = "sha256:" + calls[i].ResultSHA256
		}
		if calls[i].Seq == 0 {
			calls[i].Seq = i + 1
		}
		if calls[i].SessionID != "" && !seen[calls[i].SessionID] {
			seen[calls[i].SessionID] = true
			if rec.SessionRef != "" {
				rec.SessionRef += ","
			}
			rec.SessionRef += calls[i].SessionID
		}
		if calls[i].RedactedCount > 0 {
			rec.SanitizationEvents = append(rec.SanitizationEvents, SanitizationEvent{
				Timestamp: calls[i].Timestamp, Count: calls[i].RedactedCount, Source: calls[i].Tool,
			})
		}
	}
	if rec.SessionRef == "" {
		rec.SessionRef = "unknown"
	}
	rec.ToolCalls = calls
	return rec
}

// Render 以人类可读文本输出决策链；不输出未脱敏的原始 args/result。
func Render(w io.Writer, rec Record) error {
	if _, err := fmt.Fprintf(w, "DecisionRecord session=%s cluster_snapshot_ref=%s llm_trace_ref=%s tool_calls=%d outcome=%q\n", rec.SessionRef, short(rec.ClusterSnapshotRef, 24), short(rec.LLMTraceRef, 24), len(rec.ToolCalls), rec.Outcome); err != nil {
		return err
	}
	for _, c := range rec.ToolCalls {
		ts := "-"
		if !c.Timestamp.IsZero() {
			ts = c.Timestamp.UTC().Format(time.RFC3339)
		}
		line := fmt.Sprintf("%2d  %-20s %-18s %-20s bytes=%-6d redacted=%-3d err=%s\n",
			c.Seq, ts, c.Tool, short(c.ArgsPreview, 20), c.ResultBytes, c.RedactedCount, c.ErrorClass)
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
		if c.ResultPreview != "" {
			if _, err := fmt.Fprintf(w, "    result: %s\n", short(c.ResultPreview, 160)); err != nil {
				return err
			}
		}
		if c.InjectionHit {
			if _, err := fmt.Fprintf(w, "    injection_hit: %s\n", strings.Join(c.InjectionPatterns, ",")); err != nil {
				return err
			}
		}
		if c.ResultSHA256 != "" {
			if _, err := fmt.Fprintf(w, "    sha256: %s\n", c.ResultSHA256); err != nil {
				return err
			}
		}
	}
	if len(rec.SanitizationEvents) > 0 {
		if _, err := fmt.Fprintf(w, "sanitization_events=%d\n", len(rec.SanitizationEvents)); err != nil {
			return err
		}
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func intOf(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func parseTime(v any) (time.Time, bool) {
	s, _ := v.(string)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func short(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

func stringsOf(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
