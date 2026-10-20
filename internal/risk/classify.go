// Package risk 实现 R0–R3 的确定性风险分级（设计文档 §4.2.2，M2 T2.2）。
//
// 分级不是 LLM 判断：输入是工具名/参数/目标对象/安全证据，输出是查表结果。
// 最终级别 = max(确定性表, 调用方提供的 risk_hint, 上下文修饰)；R3 为代码级硬拒，
// 没有审批入口（I1）。本包纯 stdlib，禁止 import k8s/OPA/MCP（depguard）。
package risk

import (
	"fmt"
	"strings"
)

// Level 是风险级别。
type Level string

// LevelR0 是只读/无影响；R1–R3 由确定性表与上下文修饰取高。
const (
	LevelR0 Level = "R0"
	LevelR1 Level = "R1"
	LevelR2 Level = "R2"
	LevelR3 Level = "R3"
)

// rank 越大越危险。
func rank(l Level) int {
	switch l {
	case LevelR0:
		return 0
	case LevelR1:
		return 1
	case LevelR2:
		return 2
	case LevelR3:
		return 3
	}
	return 3 // 未知级别 fail-closed 按最高
}

// Max 返回更高级别；未知级别按 R3。
func Max(a, b Level) Level {
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

// BlastRadius 是风险分级的影响面输入（与 ChangeRequest 的 estBlastRadius 对齐）。
type BlastRadius struct {
	Pods       int
	Nodes      int
	Namespaces int
}

// Input 是一次写/读操作的分级输入。Tool 是工具名；Resource/Verb/Namespace 是
// 目标对象事实；Owner 是字段所有权（HPA/ArgoCD 等）；HasInverse/DryRunPassed
// 是 I5 证据；InjectionHit 来自 I4 sanitizer（命中则衍生动作升 R2）。
type Input struct {
	Tool         string
	Verb         string
	Resource     string
	Namespace    string
	Name         string
	Args         map[string]any
	Hint         Level
	BlastRadius  BlastRadius
	Owner        string
	HasInverse   bool
	DryRunPassed bool
	InjectionHit bool
	// QuotaExceeded 由 GuardrailPolicy 配额检查给出；true 时至少 R2，最终由 policy 拒绝。
	QuotaExceeded bool
}

// Decision 是分级结果；Reasons 必须可审计（不含原始 Secret/日志文本）。
type Decision struct {
	Level          Level
	HardDeny       bool
	RequiresHuman  bool
	AutoExecutable bool
	Reasons        []string
}

func (d *Decision) add(l Level, reason string) {
	if rank(l) > rank(d.Level) {
		d.Level = l
	}
	d.Reasons = append(d.Reasons, reason)
}

// Classify 执行确定性分级。任何未知/缺证据都向更保守方向收敛：
// 无逆操作、dry-run 未过、所有权非空、注入命中、配额超限 → 至少 R2。
func Classify(in Input) Decision {
	d := Decision{Level: LevelR0}
	if in.Hint != "" {
		d.add(in.Hint, "tool risk_hint="+string(in.Hint))
	}
	tool := strings.ToLower(in.Tool)
	verb := strings.ToLower(in.Verb)
	res := strings.ToLower(in.Resource)
	ns := strings.ToLower(in.Namespace)
	args := strings.ToLower(fmt.Sprint(in.Args))

	isRead := isReadOnly(tool, verb)
	// --- R3 硬拒：代码级，不可被策略放宽 ---
	// 保护命名空间只拦截写；读 kube-system 的诊断是合法路径。
	if !isRead && isProtectedNamespace(ns) {
		d.add(LevelR3, "目标命名空间属于 R3 保护域："+ns)
		d.HardDeny = true
	}
	if res == "namespaces" && (verb == "delete" || verb == "update" || verb == "patch") {
		d.add(LevelR3, "删除/修改 namespace 是 R3 硬拒")
		d.HardDeny = true
	}
	if res == "secrets" && verb != "list" && verb != "get" {
		d.add(LevelR3, "写 Secret 是 R3 硬拒")
		d.HardDeny = true
	}
	if !isRead && (res == "clusterroles" || res == "clusterrolebindings") {
		d.add(LevelR3, "修改集群 RBAC 是 R3 硬拒")
		d.HardDeny = true
	}
	if strings.Contains(tool, "shell") || strings.Contains(tool, "exec") ||
		strings.Contains(args, "hostpath") || strings.Contains(args, "privileged:true") {
		d.add(LevelR3, "shell/exec/hostPath/privileged 是 R3 硬拒")
		d.HardDeny = true
	}
	if strings.Contains(tool, "get_secret") || strings.Contains(tool, "read_secret") {
		d.add(LevelR3, "Secret 明文读取是 R3 硬拒")
		d.HardDeny = true
	}

	// --- 基础 R0/R1/R2 表 ---
	switch {
	case isRead:
		d.add(LevelR0, "只读工具/动词")
	case isScale(tool):
		delta := scaleDelta(in.Args)
		if delta == 0 || delta > 1 || delta < -1 {
			d.add(LevelR2, fmt.Sprintf("scale 步长 %d 超出 R1 的 ±1", delta))
		} else {
			d.add(LevelR1, "scale ±1 属 R1 可逆小写")
		}
	case tool == "restart_pod" || (tool == "restart" && res == "pod"):
		d.add(LevelR1, "单 Pod restart 属 R1")
	case tool == "cordon_node" || tool == "uncordon_node":
		d.add(LevelR1, "单节点 cordon/uncordon 属 R1")
	case tool == "restart_deployment" || tool == "rollout_restart":
		d.add(LevelR2, "滚动重启影响整个工作负载")
	case verb == "delete" || verb == "patch" || verb == "create" || verb == "apply" || verb == "update":
		d.add(LevelR2, "非 R1 白名单的写动词")
	default:
		// 未知工具 fail-closed 到 R2，但 R3 硬拒仍优先。
		d.add(LevelR2, "未知工具/动词按 R2 保守处理")
	}

	// 读操作没有 I5/所有者/爆炸半径语义，直接返回（Secret 读取已被上面硬拒）。
	if isRead {
		d.RequiresHuman = false
		d.AutoExecutable = false
		return d
	}

	// --- 上下文修饰：只升不降；只对写操作生效 ---
	if in.Owner != "" {
		d.add(LevelR2, "目标字段有控制器所有权（"+in.Owner+"），需 owner API/人审")
	}
	if in.InjectionHit {
		d.add(LevelR2, "I4 注入命中：该数据衍生的动作自动升 R2")
	}
	if !in.HasInverse {
		d.add(LevelR2, "无逆操作，I5 要求升 R2")
	}
	if !in.DryRunPassed {
		d.add(LevelR2, "dry-run 未通过/未执行")
	}
	if in.QuotaExceeded {
		d.add(LevelR2, "爆炸半径/配额超限")
	}
	if in.BlastRadius.Namespaces > 1 || in.BlastRadius.Nodes > 1 || in.BlastRadius.Pods > 10 {
		d.add(LevelR2, fmt.Sprintf("影响面超出口径：%+v", in.BlastRadius))
	}

	d.RequiresHuman = rank(d.Level) >= rank(LevelR2)
	d.AutoExecutable = d.Level == LevelR1 && !d.HardDeny && in.HasInverse && in.DryRunPassed && in.Owner == "" && !in.InjectionHit
	return d
}

func isProtectedNamespace(ns string) bool {
	switch ns {
	case "kube-system", "kube-public", "kube-node-lease", "gatekeeper-system", "aegis-system":
		return true
	}
	return false
}

func isReadOnly(tool, verb string) bool {
	if verb == "get" || verb == "list" || verb == "watch" || verb == "describe" {
		return true
	}
	switch tool {
	case "get_pods", "get_deployments", "get_nodes", "get_events", "get_pod_logs",
		"get_resource_metrics", "describe_pod", "describe_deployment", "scan_cluster",
		"prom_query", "prom_range", "aggregate_logs", "get_event_timeline":
		return true
	}
	return false
}

func isScale(tool string) bool {
	return tool == "scale_deployment" || tool == "scale_statefulset" || tool == "scale"
}

func scaleDelta(args map[string]any) int {
	// 只支持显式 delta 或 replicas 变更；无法解析返回 0（按 R2 保守处理）。
	if args == nil {
		return 0
	}
	if v, ok := args["delta"]; ok {
		if f, ok := toInt(v); ok {
			return f
		}
	}
	from, ok1 := toInt(args["replicas_from"])
	if !ok1 {
		from, ok1 = toInt(args["from_replicas"])
	}
	to, ok2 := toInt(args["replicas"])
	if ok1 && ok2 {
		return to - from
	}
	return 0
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
