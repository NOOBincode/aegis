# scan 双层规则库契约（A 层 Detector + B 层 YAML DSL）

> 可执行契约：签名、字段、校验矩阵、正误对比、测试点。改规则库前必读。

## 1. Scope / Trigger

新增/修改 A 层故障 detector、B 层 YAML 规则、Finding/Snapshot 契约时适用。属限界上下文内部契约变更。

## 2. 核心签名

```go
// A 层：运行时故障根因（硬编码 Go，一模式一 struct）
type Detector interface {
    ID() string                       // 如 "OOM_KILLED"，用于 Finding.RuleID 与审计
    Detect(facts PodFacts) *Finding   // 纯函数；nil = "不是我的问题"
}
func DefaultDetectors(now func() time.Time) []Detector  // 注册表，注入时钟保确定性
func Run(detectors []Detector, pods []PodFacts, workers int) []Finding  // 按 Pod 有界并行

// B 层：结构缺陷 lint（数据驱动 YAML，可热修改）
type PolicyRule struct {
    ID      string        // ^[A-Za-z0-9._-]+$
    Match   PolicyMatch   // {Kind, Namespaces}（Kind 白名单，排除 Secret/ConfigMap）
    Assert  PolicyAssert  // {Path(白名单), Op(封闭集), Values}
    Level   Severity      // info|warning|critical
    Message string
}
func ValidateRuleSet(rules []PolicyRule) error          // 加载期 fail-fast 强校验
func EvaluatePolicies(rules []PolicyRule, s Snapshot, workers int) []Finding  // 纯函数

// 引擎编排（无状态，policies 经参数传入）
func (e *Engine) Scan(s Snapshot, policies []PolicyRule) Result
```

## 3. 契约要点

### Finding 字段
- `ID` 稳定标识 `<rule>/<obj>`（如 `OOM_KILLED/Pod/shop/payments-0`），供审计/回归比对。
- `RuleID`：A 层为 detector id；B 层为 `policy/<ruleID>`（前缀区分来源）。
- `Severity`：detector 命中一律 `critical`；B 层按规则 `level`。
- `Confidence`：直读 K8s 状态 = `high`；启发式/统计推断 = `medium`。
- `NextTier`：`false`=规则结案（默认）；残差显式置 `true` 上送。

### OOM 分级判定（K8s 常不标 OOMKilled，只给裸退出码）
| 置信度 | 信号 | 成因佐证 |
| --- | --- | --- |
| High | `TerminatedReason=="OOMKilled"`（kubelet 标注） | 直读 |
| Medium | 仅退出码 `137`（128+SIGKILL），无 Reason | 节点 MemoryPressure/SystemOOM→节点级系统 OOM；容器设 limit→容器级 cgroup OOM |

**退出码解读表**（`exitCodeMeaning`）：137=SIGKILL(OOM/kill -9)、143=SIGTERM(滚动更新/驱逐)、139=SIGSEGV(段错误)、127=命令未找到、134=SIGABRT、1=通用错误(shell 257 回绕成 1)。

### B 层封闭操作集（故意封闭，防表达式语言膨胀）
`exists / notExists / in / notIn / matches / notMatches / gt / gte / lt / lte`（数值支持 quantity：`500m`/`512Mi`/`1.5Gi`）。**op 语义 = "应当满足"（违反即产出 finding）**。

## 4. 校验矩阵（PolicyRule.Validate，fail-fast）

| 条件 | 错误 |
| --- | --- |
| id 不匹配 `^[A-Za-z0-9._-]+$` | 拒绝 |
| kind ∈ {Secret, ConfigMap} | 拒绝（数据最小化防泄密） |
| kind ≠ Deployment（StatefulSet/DaemonSet/Job/CronJob 暂不支持） | 拒绝（Snapshot 只采集 Deployment；禁止"校验通过但 EvaluatePolicies 永不执行"的静默规则） |
| op 不在封闭集 | 拒绝 |
| path 不在白名单（containers 的 limits/probes/privileged/imageTag 等） | 拒绝 |
| gt/gte/lt/lte 的 values 不可解析为 quantity | 拒绝（防静默失效） |
| 跨规则重复 id | 拒绝（ValidateRuleSet） |

## 5. Good / Base / Bad

- **Good**：OOMKilled 容器只产 `OOM_KILLED` 一条（High）；裸 137 + 节点压力产一条 medium 并附节点级佐证。
- **Base**：CrashLoop 容器（非 OOM）产 `CRASH_LOOP`；Pending Unschedulable 产 `PENDING_UNSCHEDULABLE`。
- **Bad**：同一 OOMKilled 容器同时产 `OOM_KILLED` + `CRASH_LOOP` + `RESTART_LOOP`（噪声三报——互斥缺失）。

## 6. 测试点（必须）

- 每 detector：命中 / 不命中 / 边界 三类表驱动用例（注入固定 `now`）。
- OOM 互斥：OOMKilled 容器**不再**报 CRASH_LOOP/RESTART_LOOP。
- 137-only 推断：节点压力佐证 vs 容器 limit 佐证 vs 无佐证 三分支。
- quantity：`500m`/`1.5Gi`/`abc`（不可解析→0 且校验拦截）。
- 默认规则包：`DefaultReliabilityYAML` 可解码 + `ValidateRuleSet` 通过 + 含预期 id（抗 R30 静默退化）。
- golden：`golden_test.go` 固化 OOM 场景结案（`NextTier=false`）。

## 7. Wrong vs Correct

#### Wrong（共享可变态，并发 Scan data race）
```go
type Engine struct{ policies []PolicyRule }        // policies 存字段
func (e *Engine) Scan(s Snapshot) Result { ... use e.policies ... }
// service 里 s.engine.policies = rules  // 并发 Scan 时 data race！
```

#### Correct（无状态，policies 经参数传入）
```go
type Engine struct{ detectors []Detector; workers int; now func() time.Time } // 只读
func (e *Engine) Scan(s Snapshot, policies []PolicyRule) Result { ... }       // 可变输入走参数
// 同一 Engine 可被任意并发 Scan 安全共享，互不干扰
```

#### Wrong（Detector 双报噪声）
```go
// crashLoopDetector 不查 OOMKilled：OOMKilled 容器既报 OOM 又报 CrashLoop
if c.WaitingReason == "CrashLoopBackOff" { /* 命中 */ }
```

#### Correct（OOM 优先互斥）
```go
if c.WaitingReason == "CrashLoopBackOff" && c.LastTerminatedReason != "OOMKilled" { /* 命中 */ }
// OOM 根因已由 OOM_KILLED 报告，CrashLoop/RestartLoop 跳过
```
