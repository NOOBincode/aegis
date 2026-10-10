package main

// scan_cluster 工具：把 internal/scan 的 Tier-0 规则巡检暴露为 MCP 只读工具（T1.12）。
// 职责边界（设计文档 §4.1）：参数校验 + 调用引擎 + 结果结构化返回，无任何放行/归因判断。
// 解耦：handler 只依赖 Scanner 接口（*scan.Service 天然满足；测试注入 fake），不碰 client-go。

import (
	"context"
	"strconv"
	"strings"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/scan"
)

// Scanner 是 scan_cluster handler 依赖的巡检接口（依赖倒置：可注入 fake 测试）。
type Scanner interface {
	Scan(ctx context.Context, opts scan.CollectOptions) (scan.Result, error)
}

const scanTimeoutMs = 30000 // 采集+判定上界（大集群给足；I10 超时 fail-closed）

// scanClusterTool 定义 scan_cluster 工具（元数据五字段强校验，RiskR0 只读）。
func scanClusterTool(s Scanner) mcpserver.Tool {
	return mcpserver.Tool{
		Name: "scan_cluster",
		Description: "Tier-0 确定性规则巡检：高频故障根因（OOM/CrashLoop/Pending/驱逐/探针等）+ 结构缺陷 lint。" +
			"离线零 LLM，能规则结案的直接返回（NextTier=false），残差才需进一步诊断。结果含盲点清单（partial_reads）。",
		InputSchema: scanClusterSchema(),
		Metadata: mcpserver.ToolMetadata{
			RiskHint:       mcpserver.RiskR0,
			Idempotent:     true,
			Reversible:     false,
			EstBlastRadius: mcpserver.BlastRadius{},
			TimeoutMs:      scanTimeoutMs,
		},
		Handler: scanClusterHandler(s),
	}
}

// scanClusterSchema 入参 JSON Schema（全部可选，窄接口 I3）。
func scanClusterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace": map[string]any{"type": "string", "description": "限定命名空间（空=全集群，仍受上限约束）"},
			"format":    map[string]any{"type": "string", "enum": []string{"json", "sarif", "text"}, "description": "输出格式（默认 json；text 附人类可读摘要）"},
			"since":     map[string]any{"type": "integer", "description": "事件时间窗（秒，仅取近期事件作佐证；0=不限）"},
		},
	}
}

// scanClusterHandler 生成 handler：解析参数 → Scanner.Scan → 结构化结果。
func scanClusterHandler(s Scanner) mcpserver.Handler {
	return func(ctx context.Context, args map[string]any) *mcpserver.Result {
		if s == nil {
			return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassInternal, false, "scanner 未装配")}
		}
		format := argStr(args, "format")
		if format == "" {
			format = "json"
		}
		if format != "json" && format != "sarif" && format != "text" {
			return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "format 须为 json|sarif|text，got %q", format)}
		}
		since := argInt64(args, "since")
		if since < 0 {
			return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "since 不能为负，got %d", since)}
		}
		res, err := s.Scan(ctx, scan.CollectOptions{
			Namespace:    argStr(args, "namespace"),
			SinceSeconds: since,
		})
		if err != nil {
			return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, true, "巡检失败: %v", err)}
		}
		return &mcpserver.Result{Data: resultData(res, format)}
	}
}

// resultData 把 scan.Result 结构化（Agent 友好）；format=text 时附人类可读摘要。
func resultData(res scan.Result, format string) map[string]any {
	crit, warn, info := 0, 0, 0
	for _, f := range res.Findings {
		switch f.Severity {
		case scan.SeverityCritical:
			crit++
		case scan.SeverityWarning:
			warn++
		case scan.SeverityInfo:
			info++
		}
	}
	data := map[string]any{
		"schema":       "scan-v1",
		"generated_at": res.GeneratedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"summary": map[string]any{
			"total_findings":    len(res.Findings),
			"critical":          crit,
			"warning":           warn,
			"info":              info,
			"scanned_pods":      res.ScannedPods,
			"resolved_by_rules": res.ResolvedByRules,
		},
		"findings":      res.Findings,
		"partial_reads": res.PartialReads,
	}
	if format == "text" {
		var sb strings.Builder
		if err := scan.WriteText(&sb, res); err == nil {
			data["report_text"] = sb.String()
		}
	}
	if format == "sarif" {
		var sb strings.Builder
		if err := scan.WriteSARIF(&sb, res); err == nil {
			data["report_sarif"] = sb.String()
		}
	}
	return data
}

// argStr 提取字符串入参（MCP args 为 map[string]any）。
func argStr(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// argInt64 提取整型入参（数字落地为 float64）。
func argInt64(args map[string]any, key string) int64 {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}
