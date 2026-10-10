package main

// k8s-read 只读工具（续）：get_resource_metrics / describe_pod / describe_deployment。

import (
	"context"
	"fmt"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/mcp-servers/k8s-read/reader"
)

func getResourceMetrics(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "get_resource_metrics",
		Description: "查询资源指标（metrics.k8s.io 数值，kubectl top 一族）。PromQL 表达式查询请用 prom server 的 query/range。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"kind"},
			"properties": map[string]any{
				"kind":  map[string]any{"type": "string", "enum": []string{"pods", "nodes"}, "description": "pods 或 nodes"},
				"ns":    map[string]any{"type": "string", "description": "命名空间（kind=pods 时必填）"},
				"limit": map[string]any{"type": "integer", "description": "返回上限，≤200"},
			},
		},
		Metadata: roMeta(8000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			kind := argStr(args, "kind")
			limit, cursor := argLimit(args), argStr(args, "cursor")
			switch kind {
			case "pods":
				ns, perr := requireStr(args, "ns")
				if perr != nil {
					return &mcpserver.Result{Err: perr}
				}
				list, err := r.PodMetrics(ctx, ns, limit, cursor)
				if err != nil {
					return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, true, "查 pod 指标失败: %v", err)}
				}
				items := make([]map[string]any, 0, len(list.Items))
				for _, pm := range list.Items {
					var cpu, mem int64
					for _, c := range pm.Containers {
						cpu += c.Usage.Cpu().MilliValue()
						mem += c.Usage.Memory().Value()
					}
					items = append(items, map[string]any{"name": pm.Name, "cpu_milli": cpu, "memory_bytes": mem})
				}
				res, _ := listData(items, list.Continue)
				return res
			case "nodes":
				list, err := r.NodeMetrics(ctx, limit, cursor)
				if err != nil {
					return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, true, "查 node 指标失败: %v", err)}
				}
				items := make([]map[string]any, 0, len(list.Items))
				for _, nm := range list.Items {
					items = append(items, map[string]any{
						"name":         nm.Name,
						"cpu_milli":    nm.Usage.Cpu().MilliValue(),
						"memory_bytes": nm.Usage.Memory().Value(),
					})
				}
				res, _ := listData(items, list.Continue)
				return res
			default:
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "kind 须为 pods|nodes，got %q", kind)}
			}
		},
	}
}

func describePod(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "describe_pod",
		Description: "描述单个 Pod（只读）。返回 name/phase/conditions/container_states/重启数等摘要。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"ns", "name"},
			"properties": map[string]any{
				"ns":   map[string]any{"type": "string", "description": "命名空间（必填）"},
				"name": map[string]any{"type": "string", "description": "Pod 名（必填）"},
			},
		},
		Metadata: roMeta(8000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			ns, perr := requireStr(args, "ns")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			name, perr := requireStr(args, "name")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			p, err := r.Pod(ctx, ns, name)
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassNotFound, false, "pod %s/%s 不存在或不可读: %v", ns, name, err)}
			}
			conds := make([]string, 0, len(p.Status.Conditions))
			for _, c := range p.Status.Conditions {
				conds = append(conds, fmt.Sprintf("%s=%s", c.Type, c.Status))
			}
			containers := make([]map[string]any, 0, len(p.Status.ContainerStatuses))
			for _, cs := range p.Status.ContainerStatuses {
				containers = append(containers, map[string]any{
					"name":     cs.Name,
					"ready":    cs.Ready,
					"restarts": cs.RestartCount,
					"image":    cs.Image,
				})
			}
			return &mcpserver.Result{Data: map[string]any{
				"name":             p.Name,
				"namespace":        p.Namespace,
				"phase":            string(p.Status.Phase),
				"node":             p.Spec.NodeName,
				"conditions":       conds,
				"container_states": containers,
				"restarts_total":   restartCount(p),
				"age":              reader.Age(p.CreationTimestamp),
			}}
		},
	}
}

func describeDeployment(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "describe_deployment",
		Description: "描述单个 Deployment（只读）。返回 name/replicas 明细/conditions/selector 等摘要。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"ns", "name"},
			"properties": map[string]any{
				"ns":   map[string]any{"type": "string", "description": "命名空间（必填）"},
				"name": map[string]any{"type": "string", "description": "Deployment 名（必填）"},
			},
		},
		Metadata: roMeta(8000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			ns, perr := requireStr(args, "ns")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			name, perr := requireStr(args, "name")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			d, err := r.Deployment(ctx, ns, name)
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassNotFound, false, "deployment %s/%s 不存在或不可读: %v", ns, name, err)}
			}
			conds := make([]string, 0, len(d.Status.Conditions))
			for _, c := range d.Status.Conditions {
				conds = append(conds, fmt.Sprintf("%s=%s(%s)", c.Type, c.Status, c.Reason))
			}
			// nil-safe：fake/边界 Deployment 可能无 Selector/Replicas。
			var desired int32
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			selector := ""
			if d.Spec.Selector != nil {
				selector = fmt.Sprintf("%v", d.Spec.Selector.MatchLabels)
			}
			return &mcpserver.Result{Data: map[string]any{
				"name":      d.Name,
				"namespace": d.Namespace,
				"replicas": map[string]any{
					"desired":     desired,
					"available":   d.Status.AvailableReplicas,
					"updated":     d.Status.UpdatedReplicas,
					"unavailable": d.Status.UnavailableReplicas,
				},
				"conditions": conds,
				"selector":   selector,
				"age":        reader.Age(d.CreationTimestamp),
			}}
		},
	}
}
