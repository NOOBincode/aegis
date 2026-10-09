package scan

import "context"

// ports.go 声明 scan 上下文的出站接口（铁律 1：适配器实现，domain/service 面向接口）。
// 实现位于 internal/adapters/k8s（client-go 封装），domain 不感知基础设施（铁律 2）。

// SnapshotProvider 取数端口：采集集群只读状态聚合为 Snapshot。
// 实现须遵守数据最小化（§4.2.2）并记录采集盲点（PartialReads，绝不静默当空）。
type SnapshotProvider interface {
	// Collect 采集快照；opts 限定范围（命名空间/上限），实现侧强制分页与上限（F5）。
	Collect(ctx context.Context, opts CollectOptions) (Snapshot, error)
}

// CollectOptions 采集范围与上限。
type CollectOptions struct {
	// Namespace 限定命名空间；空=全集群（适配层仍需分页上限）。
	Namespace string
	// MaxObjects 单类对象采集上限（≤200，对齐 §4.1）。
	MaxObjects int
	// SinceSeconds 事件时间窗（仅取近期事件）。
	SinceSeconds int64
}

// PolicySource B 层规则来源端口：返回【已解析】的 PolicyRule 集（YAML 解码在适配/装配边缘）。
// 热修改语义：每次 Scan 前调用 Load 重新读取，实现侧负责 fail-safe（坏规则回退良好集）。
type PolicySource interface {
	// Load 读取当前规则集并强校验；加载失败返回 (上一份良好集, err)，绝不带病运行。
	Load() ([]PolicyRule, error)
}
