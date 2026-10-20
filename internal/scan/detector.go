package scan

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Detector 是 A 层运行时故障根因判定（借鉴 kubeagent diagnose.Detector，纯自研）。
// 纯函数：输入单 Pod 的 PodFacts，命中返回 *Finding，未命中返回 nil（"不是我的问题"）。
// 一故障模式一实现；新增模式 = 一个 struct + DefaultDetectors 注册一行，不改引擎。
type Detector interface {
	// ID 规则标识（如 "OOM_KILLED"），用于 Finding.RuleID 与审计。
	ID() string
	// Detect 判定单个 Pod；now 由注册表注入保确定性（Fuzz/golden test 依赖）。
	Detect(facts PodFacts) *Finding
}

// DefaultDetectors 返回 A 层注册表（注入时钟保确定性）。顺序即优先级。
// 覆盖设计 8 类高频故障模式：OOM_KILLED/EVICTED/IMAGE_PULL/CRASH_LOOP/PENDING_UNSCHEDULABLE
// + PROBE_FAILING/RESTART_LOOP/CONTAINER_START_ERROR/CONFIG_ERROR。
func DefaultDetectors(now func() time.Time) []Detector {
	return []Detector{
		oomKilledDetector{now: now},
		evictedDetector{},
		imagePullDetector{},
		configErrorDetector{},
		containerStartErrorDetector{},
		crashLoopDetector{now: now},
		probeFailingDetector{},
		restartLoopDetector{threshold: restartLoopThreshold},
		pendingUnschedulableDetector{},
	}
}

// Run 把每个 Detector 应用到每个 Pod 的 PodFacts，聚合命中结果。
//
// 并发（并行安全 + 任务隔离）：按 Pod 有界并行扇出——每个 goroutine 只处理一个 Pod、
// 只写自己的结果槽位（pmap 按索引隔离，零共享写）；Detector 为无状态纯函数，
// 多 goroutine 共享只读，天然安全。输出按 Finding.ID 确定性排序（与并行度无关）。
func Run(detectors []Detector, pods []PodFacts, workers int) []Finding {
	per := pmap(workers, pods, func(facts PodFacts) []Finding {
		hits := make([]Finding, 0, len(detectors))
		for _, d := range detectors {
			if f := d.Detect(facts); f != nil {
				hits = append(hits, *f)
			}
		}
		return hits
	})
	out := make([]Finding, 0, len(pods))
	for _, hits := range per {
		out = append(out, hits...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// podRef 派生 Pod 的 ObjRef。
func podRef(p PodInfo) ObjRef {
	return ObjRef{Kind: "Pod", Namespace: p.Namespace, Name: p.Name}
}

// 证据截断上限（对齐 kubeagent evidenceLimit，防不可信长文本进报告）。
const evidenceMaxLen = 120

// clip 截断不可信文本到上限（I4 数据最小化的判定侧）。
func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", " ") // 单行化
	if len(s) > evidenceMaxLen {
		return s[:evidenceMaxLen] + "…"
	}
	return s
}

// ---- OOM_KILLED ----

// exitOOMKill 被 OOM killer（SIGKILL）终止的退出码：128+9=137。
const exitOOMKill = 137

// oomKilledDetector 判定内存耗尽终止。分级（对齐 kubeagent confidence 模型）：
//
//	High（直读信号）：container 终止 Reason=="OOMKilled"——kubelet 已明确标注。
//	Medium（推断信号）：无明确 Reason，仅见退出码 137（128+SIGKILL）。
//	  K8s 很多场景只暴露裸退出码而无可读报错（尤其节点级系统 OOM），此时用
//	  佐证信号区分两种成因——
//	    容器级 cgroup OOM：容器设了 memory limit（超限被杀）；
//	    节点级系统 OOM：所在节点 MemoryPressure 或近期 SystemOOM 事件 / Pod 被驱逐。
type oomKilledDetector struct{ now func() time.Time }

func (d oomKilledDetector) ID() string { return "OOM_KILLED" }

func (d oomKilledDetector) Detect(facts PodFacts) *Finding {
	for _, c := range facts.containers() {
		// 高置信：kubelet 明确标注 OOMKilled。
		if c.TerminatedReason == "OOMKilled" || c.LastTerminatedReason == "OOMKilled" {
			f := newFinding(d.ID(), podRef(facts.Pod), SeverityCritical,
				"容器 "+c.Name+" 因内存超限被 OOMKilled（kubelet 标注）")
			d.addEvidence(&f, c)
			f.Suggestion = "提高 memory limit 或排查内存泄漏；若为 HPA 管辖字段，改 HPA/VPA 而非直改 Deployment（I9）"
			return &f
		}
		// 中置信：仅见退出码 137、无 kubelet OOMKilled 标注——推断 OOM，附成因佐证。
		// 注意：kind/cgroup v2 下真实 cgroup OOM 也可能只报 reason=Error；137 本身
		// 只证明 SIGKILL，不能 critical 硬判。规则降级为 Warning + NextTier=true，
		// 由 Agent 用 reason/指标/事件复核，避免把子进程 kill / 脚本自杀当容器 OOM。
		lastReason := c.LastTerminatedReason
		if lastReason == "" {
			lastReason = c.TerminatedReason
		}
		if lastReason != "" && lastReason != "Error" {
			continue
		}
		if c.LastTerminatedCode == exitOOMKill || c.TerminatedCode == exitOOMKill {
			f := newFinding(d.ID(), podRef(facts.Pod), SeverityWarning,
				"容器 "+c.Name+" 退出码 137（SIGKILL），疑似内存耗尽被杀")
			f.Confidence = ConfidenceMedium // 推断信号，非直读
			f.NextTier = true               // 残差需 L3 用 reason/指标复核，不能规则结案
			d.addEvidence(&f, c)
			if lastReason == "Error" {
				f.Evidence = append(f.Evidence, "kubelet reason=Error（非 OOMKilled）；137 仅证明 SIGKILL，需 L3 复核")
			}
			switch {
			case facts.NodeSystemOOM || facts.NodeMemoryPressure:
				f.Evidence = append(f.Evidence, "节点级佐证：节点内存压力/SystemOOM（疑节点级 OOM，非容器超 limit）")
				f.Suggestion = "节点内存耗尽：排查节点上内存大户、检查 kubelet 预留与驱逐阈值；必要时迁走/扩容节点"
			case c.MemoryLimitBytes > 0:
				f.Evidence = append(f.Evidence, "容器级佐证：设了 memory limit="+formatBytes(c.MemoryLimitBytes)+"，疑容器超限被杀")
				f.Suggestion = "提高 memory limit 或排查内存泄漏"
			default:
				f.Suggestion = "未设 memory limit 且无节点压力佐证：核对容器日志与节点 dmesg/oom_kill 记录"
			}
			return &f
		}
	}
	return nil
}

// addEvidence 填充 OOM 判定的通用证据（退出码含义 + 终止时间 + limit）。
func (d oomKilledDetector) addEvidence(f *Finding, c ContainerInfo) {
	if code := lastExitCode(c); code != 0 {
		f.Evidence = append(f.Evidence, "退出码="+itoa32(code)+"（"+exitCodeMeaning(code)+"）")
	}
	if !c.LastTerminatedAt.IsZero() {
		f.Evidence = append(f.Evidence, "上次终止于 "+d.now().Sub(c.LastTerminatedAt).Round(time.Second).String()+" 前")
	}
	if c.RestartCount > 0 {
		f.Evidence = append(f.Evidence, "重启次数="+itoa(c.RestartCount))
	}
}

// lastExitCode 取容器最近一次终止退出码（优先 LastTerminated）。
func lastExitCode(c ContainerInfo) int32 {
	if c.LastTerminatedCode != 0 {
		return c.LastTerminatedCode
	}
	return c.TerminatedCode
}

// exitCodeMeaning 把裸退出码翻译成人话（K8s 常只暴露数字而无报错正文）。
func exitCodeMeaning(code int32) string {
	switch code {
	case 0:
		return "正常退出"
	case 1:
		return "通用应用错误（catchall）；注意 shell 中 257 会回绕成 1"
	case 126:
		return "命令存在但不可执行（权限/解释器问题）"
	case 127:
		return "命令未找到（镜像内缺二进制/入口写错）"
	case 128:
		return "非法退出参数"
	case exitOOMKill: // 137
		return "128+9=SIGKILL，被强制杀死——常见于 OOM 或人为 kill -9"
	case 143:
		return "128+15=SIGTERM，被优雅终止信号杀死（滚动更新/驱逐/缩容）"
	case 139:
		return "128+11=SIGSEGV，段错误（程序内存访问越界，非内存耗尽）"
	case 134:
		return "128+6=SIGABRT，程序主动中止（如 Go panic 未捕获）"
	case 255:
		return "退出码越界/255（shell 对 >255 截断）"
	default:
		if code > 128 && code < 192 {
			return "128+" + itoa32(code-128) + "（被信号杀死）"
		}
		return "应用自定义退出码"
	}
}

// ---- EVICTED ----

// evictedDetector 判定 Pod 被驱逐（Phase=Failed 且 Reason=Evicted）。
// 驱逐多由节点资源压力触发（DiskPressure/MemoryPressure），常与系统级 OOM 同根。
type evictedDetector struct{}

func (d evictedDetector) ID() string { return "EVICTED" }

func (d evictedDetector) Detect(facts PodFacts) *Finding {
	p := facts.Pod
	if p.Phase != "Failed" || p.Reason != "Evicted" {
		return nil
	}
	f := newFinding(d.ID(), podRef(p), SeverityCritical, "Pod 被驱逐（Evicted）")
	if facts.NodeMemoryPressure {
		f.Evidence = append(f.Evidence, "节点佐证：MemoryPressure（疑内存压力触发驱逐）")
		f.Confidence = ConfidenceHigh
	} else {
		f.Confidence = ConfidenceMedium
	}
	if p.Message != "" {
		f.Evidence = append(f.Evidence, clip(p.Message))
	}
	f.Suggestion = "查节点压力（磁盘/内存）与驱逐阈值；为该工作负载设 resources 与 QoS 以降低被驱逐优先级"
	return &f
}

// ---- IMAGE_PULL ----

// imagePullDetector 判定镜像拉取失败（Waiting Reason ∈ ImagePullBackOff/ErrImagePull）。
type imagePullDetector struct{}

func (d imagePullDetector) ID() string { return "IMAGE_PULL_ERROR" }

func (d imagePullDetector) Detect(facts PodFacts) *Finding {
	for _, c := range facts.containers() {
		if c.WaitingReason != "ImagePullBackOff" && c.WaitingReason != "ErrImagePull" {
			continue
		}
		f := newFinding(d.ID(), podRef(facts.Pod), SeverityCritical,
			"容器 "+c.Name+" 镜像拉取失败（"+c.WaitingReason+"）")
		f.Suggestion = "核对镜像名/tag、镜像仓库可达性与 imagePullSecret；多工作负载同一仓库失败→疑仓库故障"
		return &f
	}
	return nil
}

// ---- CRASH_LOOP ----

// crashLoopDetector 判定 CrashLoopBackOff（Waiting Reason=="CrashLoopBackOff"）。
// 与 OOM 互斥：若容器上次终止是 OOMKilled，OOM_KILLED 已命中根因，这里跳过避免噪声双报。
type crashLoopDetector struct{ now func() time.Time }

func (d crashLoopDetector) ID() string { return "CRASH_LOOP" }

func (d crashLoopDetector) Detect(facts PodFacts) *Finding {
	for _, c := range facts.containers() {
		if c.WaitingReason != "CrashLoopBackOff" {
			continue
		}
		if c.LastTerminatedReason == "OOMKilled" {
			continue // 根因是 OOM，由 OOM_KILLED 报告，不在此重复
		}
		f := newFinding(d.ID(), podRef(facts.Pod), SeverityCritical,
			"容器 "+c.Name+" 反复崩溃（CrashLoopBackOff）")
		f.Evidence = append(f.Evidence, "重启次数="+itoa(c.RestartCount))
		if c.LastTerminatedCode != 0 {
			f.Evidence = append(f.Evidence, "上次退出码="+itoa32(c.LastTerminatedCode)+"（"+exitCodeMeaning(c.LastTerminatedCode)+"）")
		}
		f.Suggestion = "查上次终止日志定位崩溃根因；退出码含义见证据"
		return &f
	}
	return nil
}

// ---- CONFIG_ERROR ----

// configErrorDetector 判定容器配置错误（CreateContainerConfigError：缺 ConfigMap/Secret/key）。
// kubelet 消息明确指向缺失的配置源，属直读信号（High）。
type configErrorDetector struct{}

func (d configErrorDetector) ID() string { return "CONFIG_ERROR" }

func (d configErrorDetector) Detect(facts PodFacts) *Finding {
	for _, c := range facts.containers() {
		if c.WaitingReason != "CreateContainerConfigError" {
			continue
		}
		f := newFinding(d.ID(), podRef(facts.Pod), SeverityCritical,
			"容器 "+c.Name+" 配置错误（CreateContainerConfigError：引用缺失的 ConfigMap/Secret/key）")
		f.Suggestion = "核对引用的 ConfigMap/Secret 是否存在、key 名是否拼对（注意键名大小写）"
		return &f
	}
	return nil
}

// ---- CONTAINER_START_ERROR ----

// containerStartErrorDetector 判定容器启动失败兜底族
// （RunContainerError/CreateContainerError/StartError）。不猜测具体原因（Medium）。
type containerStartErrorDetector struct{}

func (d containerStartErrorDetector) ID() string { return "CONTAINER_START_ERROR" }

// containerStartWaitingReasons 容器启动失败的 waiting 原因集合（兜底，config 错误由 CONFIG_ERROR 单列）。
var containerStartWaitingReasons = map[string]bool{
	"RunContainerError":    true,
	"CreateContainerError": true,
	"StartError":           true,
}

func (d containerStartErrorDetector) Detect(facts PodFacts) *Finding {
	for _, c := range facts.containers() {
		if !containerStartWaitingReasons[c.WaitingReason] {
			continue
		}
		f := newFinding(d.ID(), podRef(facts.Pod), SeverityCritical,
			"容器 "+c.Name+" 无法启动（"+c.WaitingReason+"）")
		f.Confidence = ConfidenceMedium // 不猜测具体原因
		f.Suggestion = "查 describe Pod 与 kubelet 事件定位启动失败原因（镜像入口/挂载/权限）"
		return &f
	}
	return nil
}

// ---- PROBE_FAILING ----

// probeFailingDetector 判定探针持续失败：Pod Running 但容器未 Ready，且有 Unhealthy(probe) 事件佐证。
// 用 PodFacts.Events（事件佐证），启发式判定（Medium，对齐 kubeagent probefailure）。
type probeFailingDetector struct{}

func (d probeFailingDetector) ID() string { return "PROBE_FAILING" }

func (d probeFailingDetector) Detect(facts PodFacts) *Finding {
	p := facts.Pod
	if p.Phase != "Running" {
		return nil
	}
	// 事件佐证：存在 Unhealthy 且 message 提及 probe failed。
	probeEvent := ""
	for _, ev := range facts.Events {
		if ev.Reason == "Unhealthy" && strings.Contains(strings.ToLower(ev.Message), "probe") {
			probeEvent = clip(ev.Message)
			break
		}
	}
	if probeEvent == "" {
		return nil
	}
	for _, c := range p.Containers {
		if c.Ready {
			continue
		}
		f := newFinding(d.ID(), podRef(p), SeverityWarning,
			"容器 "+c.Name+" 运行中但探针持续失败（未就绪）")
		f.Confidence = ConfidenceMedium
		f.Evidence = append(f.Evidence, probeEvent)
		f.Suggestion = "核对 readiness/liveness 探针的路径/端口/超时与启动耗时；启动慢的加大 initialDelaySeconds"
		return &f
	}
	return nil
}

// ---- RESTART_LOOP ----

// restartLoopThreshold 反复重启判定阈值（对齐 kubeagent RestartThreshold=3）。
const restartLoopThreshold = 3

// restartLoopDetector 判定运行中反复重启（Running 但 RestartCount≥阈值且仍在 flap）。
// 与 OOM/CrashLoop 互斥：OOM/Waiting 态由对应 detector 报，这里只兜"运行中反复重启且未进 waiting 背刺"。
type restartLoopDetector struct{ threshold int }

func (d restartLoopDetector) ID() string { return "RESTART_LOOP" }

func (d restartLoopDetector) Detect(facts PodFacts) *Finding {
	p := facts.Pod
	if p.Phase != "Running" {
		return nil
	}
	for _, c := range facts.containers() {
		// 仅兜"当前在运行（非 waiting）但反复重启"且非 OOM 根因的情形。
		if c.RestartCount < d.threshold || c.WaitingReason != "" || c.LastTerminatedReason == "OOMKilled" {
			continue
		}
		f := newFinding(d.ID(), podRef(p), SeverityWarning,
			"容器 "+c.Name+" 反复重启（"+itoa(c.RestartCount)+" 次）")
		f.Confidence = ConfidenceMedium // 启发式
		if c.LastTerminatedCode != 0 {
			f.Evidence = append(f.Evidence, "上次退出码="+itoa32(c.LastTerminatedCode)+"（"+exitCodeMeaning(c.LastTerminatedCode)+"）")
		}
		f.Suggestion = "查历史终止日志定位反复重启根因；关注探针误杀与资源争抢"
		return &f
	}
	return nil
}

// ---- PENDING_UNSCHEDULABLE ----

// pendingUnschedulableDetector 判定 Pending 不可调度（Phase=Pending 且 PodScheduled=False/Unschedulable）。
// kubeagent 仅到 Unschedulable+调度器 message；更细的资源/污点/亲和性拆解为后续补强项。
type pendingUnschedulableDetector struct{}

func (d pendingUnschedulableDetector) ID() string { return "PENDING_UNSCHEDULABLE" }

func (d pendingUnschedulableDetector) Detect(facts PodFacts) *Finding {
	p := facts.Pod
	if p.Phase != "Pending" || p.ScheduledConditionStatus != "False" {
		return nil
	}
	f := newFinding(d.ID(), podRef(p), SeverityCritical,
		"Pod 不可调度（"+orDefault(p.ScheduledConditionReason, "Unschedulable")+"）")
	if p.ScheduledConditionMessage != "" {
		f.Evidence = append(f.Evidence, clip(p.ScheduledConditionMessage))
	}
	f.Suggestion = "核对节点资源/污点/亲和性与 PVC 绑定；必要时扩节点或放宽约束"
	return &f
}

// ---- helpers ----

// allContainers 合并 init 与常规容器（init 容器用同一套信号判定，对齐 kubeagent initcontainer.go）。
func allContainers(p PodInfo) []ContainerInfo {
	out := make([]ContainerInfo, 0, len(p.Containers)+len(p.InitContainers))
	out = append(out, p.Containers...)
	out = append(out, p.InitContainers...)
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func itoa(n int) string     { return strconv.Itoa(n) }
func itoa32(n int32) string { return strconv.FormatInt(int64(n), 10) }

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return strconv.FormatInt(b, 10) + "B"
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatInt(b/div, 10) + string("KMGTPE"[exp]) + "iB"
}
