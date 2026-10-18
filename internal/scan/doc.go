// Package scan 是 aegis 的确定性规则巡检内核（I11 Tier-0，设计文档 §4.2.2）。
//
// 定位：三级诊断流水线（Tier-0 规则 → Tier-1 归因 → Tier-2 LLM）的第一层，
// 是 LLM/人审的前置过滤器——能规则结案的就不惊动上层，以此减少 AI 审核压力。
//
// 纪律（aegis-ddd-layout 五条铁律 + depguard 强制）：
//   - 领域纯净（铁律 2）：本包为限界上下文的 domain 层，禁止 import k8s.io/*、
//     sigs.k8s.io/*、OPA、MCP SDK 与 internal/adapters/*；引擎为纯函数，可表驱动单测。
//   - 离线零 LLM 零 token（I11/I2）：无任何外呼，断网/气隙照常工作；不进热路径。
//   - 只读（I1/I3）：永不触达写能力，Suggestion 仅为提示不触发动作。
//   - 不可信数据（I4）：Snapshot 中的集群自由文本视为不可信，输出前经消毒截断。
//
// 规则库双层（借鉴 kubeagent 已验证形态，纯自研、零外部依赖）：
//   - A 层 Detector：硬编码 Go，运行时故障根因判定（Detect(PodFacts) *Finding）。
//   - B 层 Policy：数据驱动 YAML 结构缺陷 lint，规则是数据可热修改（抗规则腐烂 R30）。
//
// 取数经 ports.go 的 SnapshotProvider 抽象，由 internal/adapters/k8s 实现（铁律 1）；
// 引擎只面向 Snapshot/Finding 纯结构，与 client-go 完全解耦。
package scan
