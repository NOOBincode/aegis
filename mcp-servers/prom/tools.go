package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aegis-dev/aegis/internal/adapters/prom"
	"github.com/aegis-dev/aegis/internal/mcpserver"
)

const (
	maxSeries     = 50
	maxRangeSpan  = time.Hour
	minRangeStep  = 15 * time.Second
	maxRangePoint = 720
)

// Querier 是工具层对 Prometheus 适配器的最小端口；测试注入 fake，避免真集群。
type Querier interface {
	Query(ctx context.Context, expr string, at time.Time) (*prom.Result, error)
	QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) (*prom.Result, error)
}

func promQueryTool(q Querier) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "prom_query",
		Description: "Prometheus instant query（只读 PromQL；返回序列数/样本数受上限约束，truncated 显式标记）",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
				"time":  map[string]any{"type": "string", "description": "RFC3339 或 Unix 秒；缺省当前"},
			},
		},
		Metadata: mcpserver.ToolMetadata{
			RiskHint: mcpserver.RiskR0, Idempotent: true, Reversible: true,
			EstBlastRadius: mcpserver.BlastRadius{}, TimeoutMs: 10000,
		},
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			expr, _ := args["query"].(string)
			at, err := parseTimeArg(args["time"])
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "time 参数非法：%v", err)}
			}
			res, err := q.Query(ctx, expr, at)
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, false, "Prometheus 查询失败：%v", err)}
			}
			return &mcpserver.Result{Data: shapeResult(res, false)}
		},
	}
}

func promRangeTool(q Querier) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "prom_range",
		Description: "Prometheus range query（区间≤1h、step≥15s、点数≤720；超限自动放大 step 并返回生效参数）",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"query", "start", "end", "step_seconds"},
			"properties": map[string]any{
				"query":        map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
				"start":        map[string]any{"type": "string", "description": "RFC3339 或 Unix 秒"},
				"end":          map[string]any{"type": "string", "description": "RFC3339 或 Unix 秒"},
				"step_seconds": map[string]any{"type": "integer", "minimum": 15, "maximum": 3600},
			},
		},
		Metadata: mcpserver.ToolMetadata{
			RiskHint: mcpserver.RiskR0, Idempotent: true, Reversible: true,
			EstBlastRadius: mcpserver.BlastRadius{}, TimeoutMs: 15000,
		},
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			expr, _ := args["query"].(string)
			start, err := parseTimeArg(args["start"])
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "start 参数非法：%v", err)}
			}
			end, err := parseTimeArg(args["end"])
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "end 参数非法：%v", err)}
			}
			stepSec, _ := intArg(args["step_seconds"])
			step := time.Duration(stepSec) * time.Second
			if !end.After(start) {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "end 必须晚于 start")}
			}
			if end.Sub(start) > maxRangeSpan {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "区间超过 1h 上限；请缩短窗口")}
			}
			if step < minRangeStep {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "step_seconds 必须 ≥15")}
			}
			// 点数超限：按 span/720 向上取整到秒（最小 15s），不静默截断。
			if points := int(end.Sub(start)/step) + 1; points > maxRangePoint {
				need := int(math.Ceil(end.Sub(start).Seconds() / maxRangePoint))
				if need < 15 {
					need = 15
				}
				step = time.Duration(need) * time.Second
			}
			res, err := q.QueryRange(ctx, expr, start, end, step)
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, false, "Prometheus range 查询失败：%v", err)}
			}
			data := shapeResult(res, true)
			data["params_resolved"] = map[string]any{
				"start": start.UTC().Format(time.RFC3339), "end": end.UTC().Format(time.RFC3339),
				"step_seconds": int(step / time.Second),
			}
			return &mcpserver.Result{Data: data}
		},
	}
}

func shapeResult(res *prom.Result, withValues bool) map[string]any {
	if res == nil {
		return map[string]any{"series": []any{}, "truncated": false}
	}
	series := append([]prom.Series(nil), res.Series...)
	// 稳定顺序：按标签串排序，保证相同数据回放顺序一致（I6）。
	sort.SliceStable(series, func(i, j int) bool { return labelKey(series[i].Metric) < labelKey(series[j].Metric) })
	truncated := false
	if len(series) > maxSeries {
		series = series[:maxSeries]
		truncated = true
	}
	out := make([]map[string]any, 0, len(series))
	for _, s := range series {
		item := map[string]any{"metric": s.Metric}
		if withValues {
			vals := make([]map[string]any, 0, len(s.Values))
			for _, p := range s.Values {
				vals = append(vals, map[string]any{"timestamp": p.Timestamp.UTC().Format(time.RFC3339Nano), "value": p.Value})
			}
			item["values"] = vals
		} else if len(s.Values) > 0 {
			p := s.Values[len(s.Values)-1]
			item["value"] = p.Value
			item["timestamp"] = p.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, item)
	}
	data := map[string]any{"result_type": res.ResultType, "series": out, "truncated": truncated}
	if truncated {
		data["next_hint"] = "序列数超过 50，用更具体的 label 过滤或缩小时间窗"
	}
	return data
}

func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte(',')
	}
	return b.String()
}

func parseTimeArg(v any) (time.Time, error) {
	s, _ := v.(string)
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec := int64(f)
		return time.Unix(sec, int64((f-float64(sec))*1e9)).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("期望 RFC3339 或 Unix 秒，got %q", s)
}

func intArg(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
