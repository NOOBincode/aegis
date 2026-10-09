package scan

import (
	"sort"
	"time"
)

// Result 是一次巡检的结构化输出（design.md §3.3 Engine.Scan 返回值）。
type Result struct {
	// Findings 命中项，按 (severity 降序, ID 升序) 确定性排序。
	Findings []Finding `json:"findings"`
	// PartialReads 采集盲点（透传 Snapshot.PartialReads，绝不静默当空）。
	PartialReads []string `json:"partial_reads,omitempty"`
	// ScannedPods 参与 A 层判定的 Pod 数（coverage 度量）。
	ScannedPods int `json:"scanned_pods"`
	// ResolvedByRules 规则结案数（NextTier=false），"规则层解决率"分子（M3 eval）。
	ResolvedByRules int `json:"resolved_by_rules"`
	// GeneratedAt 生成时间（注入时钟保确定性测试）。
	GeneratedAt time.Time `json:"generated_at"`
}

// Engine 编排 A 层 Detector 与 B 层 Policy 规则，产出确定性 Result。
//
// 并发安全（关键）：Engine 是**无状态/不可变**的——detectors 与 workers 装配期固定、
// Scan 期间只读；每次调用的可变输入（B 层 policies、Snapshot）经参数传入而非存字段。
// 因此同一 Engine 可被任意并发 Scan 安全共享，多次调用互不干扰（修复了 policies
// 存字段导致的 data race）。纯领域：无任何 IO/外呼（I11/I2），可表驱动单测。
type Engine struct {
	detectors []Detector
	workers   int
	now       func() time.Time
}

// NewEngine 构造引擎。detectors 为 nil 时用 DefaultDetectors(now)；
// workers ≤0 时用 defaultScanWorkers；now 为 nil 时用 time.Now。
// policies 不在此装配——B 层规则是每次调用的可变输入，经 Scan 传入以支持热修改。
func NewEngine(detectors []Detector, workers int, now func() time.Time) *Engine {
	if now == nil {
		now = time.Now
	}
	if detectors == nil {
		detectors = DefaultDetectors(now)
	}
	if workers <= 0 {
		workers = defaultScanWorkers
	}
	return &Engine{detectors: detectors, workers: workers, now: now}
}

// Scan 对一个 Snapshot 应用全部规则（A 层 detectors + 本次传入的 B 层 policies），
// 返回确定性排序的 Result。policies 由调用方（service）在每次调用时装载——这是
// 热修改的实现点：规则变更即下一次 Scan 生效，Engine 自身不缓存规则。
func (e *Engine) Scan(s Snapshot, policies []PolicyRule) Result {
	// 事件预索引（O(events) 一次建索引，替代逐 Pod 全扫的 O(pods×events)）。
	byPod := indexEventsByPod(s.Events)
	// 节点佐证索引：内存压力状态 + 近期 SystemOOM 事件（供"无明确报错时的 OOM 推断"）。
	nodePressure := indexNodeMemoryPressure(s.Nodes)
	nodeSystemOOM := indexNodeSystemOOM(s.Events)

	// A 层：运行时故障根因（按 Pod 有界并行）。构造 PodFacts 时预合并容器（All），
	// 避免每个 detector 对同一 Pod 重复合并 init+常规容器（消除 d×p 次重复分配）。
	podFacts := make([]PodFacts, 0, len(s.Pods))
	for _, p := range s.Pods {
		podFacts = append(podFacts, PodFacts{
			Pod:                p,
			Events:             byPod[podKey(p.Namespace, p.Name)],
			NodeMemoryPressure: nodePressure[p.NodeName],
			NodeSystemOOM:      nodeSystemOOM[p.NodeName],
			All:                allContainers(p),
		})
	}
	findings := Run(e.detectors, podFacts, e.workers)

	// B 层：结构缺陷 lint（按 Deployment 有界并行）。
	findings = append(findings, EvaluatePolicies(policies, s, e.workers)...)

	// 统一消毒（I4）+ 确定性排序（severity 降序，再按 ID）。
	for i := range findings {
		findings[i] = sanitizeFinding(findings[i])
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity.rank() != findings[j].Severity.rank() {
			return findings[i].Severity.rank() > findings[j].Severity.rank()
		}
		return findings[i].ID < findings[j].ID
	})

	resolved := 0
	for _, f := range findings {
		if !f.NextTier {
			resolved++
		}
	}
	return Result{
		Findings:        findings,
		PartialReads:    s.PartialReads,
		ScannedPods:     len(s.Pods),
		ResolvedByRules: resolved,
		GeneratedAt:     e.now(),
	}
}

// podKey 事件索引键（ns + 名字）。
func podKey(ns, name string) string { return ns + "/" + name }

// indexEventsByPod 把事件按所属 Pod 预建索引（InvolvedKind=Pod）。
// 一次 O(events) 遍历建表，供逐 Pod O(1) 取用，替代嵌套全扫。
func indexEventsByPod(events []EventInfo) map[string][]EventInfo {
	byPod := make(map[string][]EventInfo, len(events))
	for _, ev := range events {
		if ev.InvolvedKind != "Pod" {
			continue
		}
		k := podKey(ev.Namespace, ev.InvolvedName)
		byPod[k] = append(byPod[k], ev)
	}
	return byPod
}

// indexNodeMemoryPressure 建"节点名 → 是否有 MemoryPressure"索引（系统 OOM 佐证）。
func indexNodeMemoryPressure(nodes []NodeInfo) map[string]bool {
	m := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		m[n.Name] = n.MemoryPressure
	}
	return m
}

// indexNodeSystemOOM 建"节点名 → 近期是否有 SystemOOM 事件"索引（节点级 OOM killer 佐证）。
func indexNodeSystemOOM(events []EventInfo) map[string]bool {
	m := make(map[string]bool)
	for _, ev := range events {
		if ev.InvolvedKind == "Node" && ev.Reason == "SystemOOM" {
			m[ev.InvolvedName] = true
		}
	}
	return m
}

// sanitizeFinding 对 Finding 的不可信自由文本做截断/单行化（I4 判定侧兜底；
// 进 LLM 上下文的整体包裹在框架中间件，本层只做字段级数据最小化）。
func sanitizeFinding(f Finding) Finding {
	f.RootCause = clip(f.RootCause)
	f.Suggestion = clip(f.Suggestion)
	for i, e := range f.Evidence {
		f.Evidence[i] = clip(e)
	}
	return f
}
