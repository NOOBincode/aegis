package scan

import "time"

// 本文件定义引擎的输入契约（design.md §3.1）。全部为 scan 自有纯结构，
// 不暴露 client-go/k8s.io 类型，使引擎与 K8s API 版本解耦（铁律 2 领域纯净）。
// 字段按规则库所需最小集裁剪（数据最小化，§4.2.2）：不含 Secret、env 明文、
// 镜像仓库路径全文等敏感面。所有自由文本字段（Message/Reason 等）视为不可信数据。

// Snapshot 是一次巡检所需的集群状态聚合（只读拷贝）。由适配层产出、引擎消费。
type Snapshot struct {
	Pods        []PodInfo        `json:"pods,omitempty"`
	Deployments []DeploymentInfo `json:"deployments,omitempty"`
	Nodes       []NodeInfo       `json:"nodes,omitempty"`
	Events      []EventInfo      `json:"events,omitempty"` // 近期 Warning 事件为主
	// PartialReads 采集盲点：被 RBAC 拒读/部分失败的资源类别。
	// 绝不静默当空——报告与 SARIF 显式渲染盲点（防"看着绿其实瞎"）。
	PartialReads []string `json:"partial_reads,omitempty"`
}

// PodFacts 是 A 层 Detector 的输入：单 Pod + 其相关事件 + 所在节点的佐证信号。
// （借鉴 kubeagent diagnose.PodFacts 并扩展节点佐证面，供"无明确报错时的 OOM 推断"）。
type PodFacts struct {
	Pod    PodInfo
	Events []EventInfo
	// NodeMemoryPressure 所在节点是否有 MemoryPressure 污点/状态（系统 OOM 佐证）。
	NodeMemoryPressure bool
	// NodeSystemOOM 所在节点近期是否有 SystemOOM 事件（节点级 OOM killer 佐证）。
	NodeSystemOOM bool
}

// ContainerInfo 单容器状态（判定 OOM/CrashLoop/探针的核心信号来源）。
type ContainerInfo struct {
	Name         string `json:"name"`
	RestartCount int    `json:"restart_count"`
	// State 当前态：waiting/running/terminated 的 Reason（如 CrashLoopBackOff）。
	WaitingReason    string `json:"waiting_reason,omitempty"`
	TerminatedReason string `json:"terminated_reason,omitempty"` // 如 OOMKilled
	TerminatedCode   int32  `json:"terminated_code,omitempty"`
	// LastTerminated* 上一次终止态（OOMKilled 常出现在这里）。
	LastTerminatedReason string    `json:"last_terminated_reason,omitempty"`
	LastTerminatedCode   int32     `json:"last_terminated_code,omitempty"`
	LastTerminatedAt     time.Time `json:"last_terminated_at,omitempty"`
	Ready                bool      `json:"ready"`
	// 资源声明（佐证 OOM/资源压力；quantity 已解析为可比数值，见 QuantityMilli）。
	MemoryLimitBytes   int64 `json:"memory_limit_bytes,omitempty"`
	MemoryRequestBytes int64 `json:"memory_request_bytes,omitempty"`
}

// PodInfo 单 Pod 最小判定面。
type PodInfo struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase"` // Pending/Running/Failed/Succeeded
	// Reason/Message 是 Pod 级终止原因（如 Evicted）。Message 为不可信自由文本。
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	NodeName  string `json:"node_name,omitempty"`
	OwnerKind string `json:"owner_kind,omitempty"` // Deployment/StatefulSet/...，无属主为空
	OwnerName string `json:"owner_name,omitempty"`
	// PodScheduled* PodScheduled condition（Pending 不可调度判定）。
	ScheduledConditionStatus  string          `json:"scheduled_condition_status,omitempty"`  // True/False/""
	ScheduledConditionReason  string          `json:"scheduled_condition_reason,omitempty"`  // 如 Unschedulable
	ScheduledConditionMessage string          `json:"scheduled_condition_message,omitempty"` // 调度器 message（不可信）
	Containers                []ContainerInfo `json:"containers,omitempty"`
	InitContainers            []ContainerInfo `json:"init_containers,omitempty"`
}

// DeploymentInfo Deployment 最小判定面。
type DeploymentInfo struct {
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
	Replicas          int32  `json:"replicas"`  // 期望
	ReadyReplicas     int32  `json:"ready"`     // 就绪
	UpdatedReplicas   int32  `json:"updated"`   // 更新中
	AvailableReplicas int32  `json:"available"` // 可用
	// Containers 模板容器（B 层结构 lint 判定资源/探针/安全姿态用）。
	Containers []WorkloadContainer `json:"containers,omitempty"`
}

// WorkloadContainer 工作负载模板容器（结构 lint 判定面，数据最小化）。
type WorkloadContainer struct {
	Name              string `json:"name"`
	Image             string `json:"image"` // tag 用于 :latest 判定；仓库路径不导出报告
	HasReadinessProbe bool   `json:"has_readiness_probe"`
	HasLivenessProbe  bool   `json:"has_liveness_probe"`
	MemoryLimitBytes  int64  `json:"memory_limit_bytes,omitempty"`
	CPULimitMilli     int64  `json:"cpu_limit_milli,omitempty"`
	Privileged        bool   `json:"privileged"`
	RunAsNonRoot      *bool  `json:"run_as_non_root,omitempty"`
}

// NodeInfo Node 最小判定面。
type NodeInfo struct {
	Name           string   `json:"name"`
	Ready          bool     `json:"ready"`
	MemoryPressure bool     `json:"memory_pressure"`
	DiskPressure   bool     `json:"disk_pressure"`
	PIDPressure    bool     `json:"pid_pressure"`
	Unschedulable  bool     `json:"unschedulable"`    // cordon
	Taints         []string `json:"taints,omitempty"` // key:effect 摘要（判定调度约束）
}

// EventInfo 事件最小判定面（Message 为不可信自由文本）。
type EventInfo struct {
	Namespace string `json:"namespace"`
	Reason    string `json:"reason"`  // 如 FailedScheduling / BackOff / Unhealthy
	Message   string `json:"message"` // 不可信，消毒截断后才进报告
	Type      string `json:"type"`    // Normal / Warning
	Count     int32  `json:"count"`
	// InvolvedObject 关联对象（用于把事件归到 PodFacts）。
	InvolvedKind string    `json:"involved_kind"`
	InvolvedName string    `json:"involved_name"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
}
