package main

// k8s-read 的 8 个只读工具（mcp-servers/k8s-read/README.md §2 工具清单）。
// 职责边界（§4.1）：参数校验 + 执行 + 结果结构化返回，不做任何放行/归因/审批判断。
// 纪律：强制分页与上限（≤200 对象 / ≤500 行日志），超限返回工具错误（上层转 429 + 分页引导）。

import (
	"context"
	"errors"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/mcp-servers/k8s-read/reader"
)

// ---- 入参提取与校验（MCP args 为 map[string]any，数字落地为 float64）----

func argStr(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// argLimit 提取 limit（可缺省；返回字符串供 reader 分页，空 = 默认上限）。
func argLimit(args map[string]any) string {
	v, ok := args["limit"]
	if !ok {
		return ""
	}
	switch n := v.(type) {
	case float64:
		return strconv.Itoa(int(n))
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case string:
		return n
	}
	return ""
}

// argInt64 提取整型入参（tail_lines / since_seconds）。兼容 float64（JSON 数字）与 int/int64（直调/测试）。
func argInt64(args map[string]any, key string) int64 {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

// toolError 把 reader 的错误映射为工具错误：入参超限是 schema 错误（不可重试），
// 其它才是下游错误（可重试）。旧实现把 limit/tail_lines 超限也标成 downstream+retryable，
// 与 §4 错误协议不符。
func toolError(err error, msg string) *mcpserver.Error {
	if errors.Is(err, reader.ErrInvalidPaging) || errors.Is(err, reader.ErrInvalidLogLimit) {
		return mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "%s: %v", msg, err)
	}
	return mcpserver.NewToolError(mcpserver.ErrClassDownstream, true, "%s: %v", msg, err)
}

// requireStr 校验必填字符串入参；缺失返回 schema 协议错误。
func requireStr(args map[string]any, key string) (string, *mcpserver.Error) {
	s := argStr(args, key)
	if s == "" {
		return "", mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "缺少必填参数 %q", key)
	}
	return s, nil
}

// listData 统一的分页返回体：items + object_count；truncated/next_cursor 进 Meta（框架层不包裹）。
func listData[T any](items []T, continueToken string) (*mcpserver.Result, error) {
	if items == nil {
		items = []T{}
	}
	res := &mcpserver.Result{
		Data: map[string]any{
			"items":        items,
			"object_count": len(items),
		},
	}
	if continueToken != "" {
		res.Meta.Truncated = true
		res.Meta.NextCursor = continueToken
	}
	return res, nil
}

// ---- 工具元数据便捷构造 ----

func roMeta(timeoutMs int) mcpserver.ToolMetadata {
	return mcpserver.ToolMetadata{
		RiskHint:       mcpserver.RiskR0,
		Idempotent:     true,
		Reversible:     false,
		EstBlastRadius: mcpserver.BlastRadius{},
		TimeoutMs:      timeoutMs,
	}
}

// registerTools 注册 k8s-read 全部 8 个只读工具。
func registerTools(reg *mcpserver.Registry, r *reader.Reader) {
	reg.MustRegister(getPods(r))
	reg.MustRegister(getDeployments(r))
	reg.MustRegister(getNodes(r))
	reg.MustRegister(getEvents(r))
	reg.MustRegister(getPodLogs(r))
	reg.MustRegister(getResourceMetrics(r))
	reg.MustRegister(describePod(r))
	reg.MustRegister(describeDeployment(r))
}

// nsObjSchema 是"ns 必填 + label_selector + limit + cursor"的公共 schema 片段。
func nsObjSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"ns"},
		"properties": map[string]any{
			"ns":             map[string]any{"type": "string", "description": "命名空间（必填）"},
			"label_selector": map[string]any{"type": "string", "description": "标签选择器，如 app=nginx"},
			"limit":          map[string]any{"type": "integer", "description": "返回上限，≤200"},
			"cursor":         map[string]any{"type": "string", "description": "分页游标（上一页 truncated 时返回的 next_cursor）"},
		},
	}
}

func getPods(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "get_pods",
		Description: "列举某命名空间下的 Pod（只读，分页）。返回 name/phase/restarts/node/age/owner_kind。",
		InputSchema: nsObjSchema(),
		Metadata:    roMeta(8000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			ns, perr := requireStr(args, "ns")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			list, err := r.Pods(ctx, ns, argStr(args, "label_selector"), argLimit(args), argStr(args, "cursor"))
			if err != nil {
				return &mcpserver.Result{Err: toolError(err, "列举 pods 失败")}
			}
			items := make([]map[string]any, 0, len(list.Items))
			for _, p := range list.Items {
				items = append(items, map[string]any{
					"name":       p.Name,
					"phase":      string(p.Status.Phase),
					"restarts":   restartCount(&p),
					"node":       p.Spec.NodeName,
					"age":        reader.Age(p.CreationTimestamp),
					"owner_kind": ownerKind(&p),
				})
			}
			res, _ := listData(items, list.Continue)
			return res
		},
	}
}

// restartCount 汇总 Pod 各容器重启次数。
func restartCount(p *corev1.Pod) int {
	n := 0
	for _, cs := range p.Status.ContainerStatuses {
		n += int(cs.RestartCount)
	}
	return n
}

// ownerKind 返回 Pod 的属主类别（Deployment/StatefulSet/...），无属主返回 ""。
func ownerKind(p *corev1.Pod) string {
	if len(p.OwnerReferences) == 0 {
		return ""
	}
	return p.OwnerReferences[0].Kind
}

// 其余工具见 tools_rest.go（为可读性拆分）。
