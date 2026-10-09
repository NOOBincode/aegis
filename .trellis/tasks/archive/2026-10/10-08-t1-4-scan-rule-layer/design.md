# T1.11 确定性规则层（scanner / Tier-0）— 技术设计

> 状态：**定稿**——复用路线已于 2026-10-08 按 kubeagent 源码调研结论（`research/reuse-decision.md`）定案。
> 配套：需求与验收见 `prd.md`；执行清单见 `implement.md`。

## 1. 设计目标与定位

实现设计文档 §4.2.2 的 `scanner` 组件——**I11 Tier-0 确定性规则巡检内核**：对集群状态做高频故障模式的确定性根因判定，全程离线、零 LLM、零 token、零 API key。它是三级诊断流水线（Tier-0 规则 → Tier-1 归因 → Tier-2 LLM）的第一层，定位是 **LLM/人审的前置过滤器**：能规则结案的就不惊动上层，以此"减少 AI 审核压力"。

本设计只覆盖 **Tier-0**；Tier-1 归因（I9，`internal/authority`）、Tier-2 LLM、写路径/审批均不在范围。

## 2. 总体架构与分层

```
        ┌─────────────────────────── 消费侧 ───────────────────────────┐
        │  aegis-cli `scan` 子命令   │   MCP 只读工具 `scan_cluster`    │
        └──────────────┬───────────────────────────┬──────────────────┘
                       │（CLI 直接调用）            │（经 internal/mcpserver 框架注册，RiskR0）
                       ▼                           ▼
        ┌──────────────────────────────────────────────────────────┐
        │  internal/scan  —— 纯领域引擎（根 module，无 client-go）     │
        │                                                          │
        │   Engine.Scan(Snapshot)                                   │
        │     ├─ A 层：Detector.Run（运行时根因，硬编码 Go）           │
        │     ├─ B 层：Policy.Evaluate（结构 lint，YAML 数据驱动）     │
        │     ├─ 聚合 + Sanitizer（I4）+ 确定性排序                   │
        │     └─ Result{findings, PartialReads 盲点, coverage}      │
        │            ▼                                             │
        │        Report（脱敏：HTML / JSON / SARIF）                  │
        └──────────────▲───────────────────────────────────────────┘
                       │ 依赖注入：SnapshotProvider 接口
        ┌──────────────┴───────────────────────────────────────────┐
        │  适配层 internal/adapters/k8s/scanprovider.go             │
        │   （client-go 唯一合法住所，实现 scan.SnapshotProvider）     │
        │   有界并发只读采集（pods/deployments/nodes/events 4 类并行） │
        │   强制分页上限 + 数据最小化 + 盲点记录 → Snapshot            │
        └──────────────────────────────────────────────────────────┘
```

> **落地校正（2026-10-10）**：适配层落在既有的 `internal/adapters/k8s`（client-go 唯一住所，铁律 2/3），而非另起 `internal/scan/k8sadapter`——架构等价且复用既有 adapter 包更贴合五条铁律。

**关键分层决策**：引擎（`internal/scan`）与取数（`internal/adapters/k8s`）分离。引擎只面向 `Snapshot` 与 `Finding` 两个纯数据结构，**不 import client-go / k8s.io**——这保证：
- 引擎可表驱动单测（注入构造好的 `Snapshot`，无需起集群）；
- 符合 depguard 纪律（domain 不依赖基础设施，`.golangci.yml` 已把 `internal/scan` 纳入铁律 2 保护）；
- 规则库可热修改、可评审、可 eval（R17 防线）。

## 3. 核心契约（数据结构）

### 3.1 Snapshot（取数输入，适配层产出）

```go
// Snapshot 是一次巡检所需的集群状态聚合（均为只读拷贝，视为不可信数据）。
type Snapshot struct {
    Pods        []PodInfo        // 含 phase/containerStatuses/restarts/lastState(OOM)/owner
    Deployments []DeploymentInfo // 含 replicas/available/conditions
    Nodes       []NodeInfo       // 含 conditions(Ready/Pressure)/taints/allocatable
    Events      []EventInfo      // 近期 Warning 事件（reason/message/count/involvedObject）
    PodMetrics  []PodMetricInfo  // metrics.k8s.io 用量（佐证 OOM/资源压力）
    // 不含 Secret、env 明文、镜像仓库路径全文等敏感面（I4 数据最小化，§4.2.2）
}
```
字段按规则库所需最小集裁剪（数据最小化）；`*Info` 为 scan 自有结构，不暴露 client-go 类型，避免引擎层耦合 k8s API 版本。

### 3.2 Finding（判定输出，引擎产出）

```go
type Finding struct {
    ID          string   // 稳定 id，如 "oomkilled/shop/payments/web-0"
    RuleID      string   // 命中的规则；A 层为 detector id，B 层为 "policy/<ruleID>" 以区分
    Severity    Severity // Info | Warning | Critical（detector 命中一律 Critical，对齐 kubeagent）
    Object      ObjRef   // {Kind, Namespace, Name}
    RootCause   string   // 确定性根因陈述（脱敏，无敏感字段）
    Evidence    []string // 支撑证据（事件/状态摘要，脱敏 + 截断）
    Confidence  Confidence // High | Medium（直读状态=high，启发式/统计推断=medium）
    Suggestion  string   // 处置建议（只读性提示，不触发写）
    NextTier    bool     // false=规则已结案（不惊动上层）；true=残差，需 Tier-1/2
}
```
`NextTier=false` 即"规则层结案"——这是减少 AI 审核压力的直接度量，喂给 M3 的"规则层解决率"指标（§4.6/R17）。

**PodFacts**（A 层 Detector 的输入，由 Snapshot 投影）：
```go
type PodFacts struct { Pod PodInfo; Events []EventInfo } // 单 Pod + 其相关事件
```

### 3.3 规则库与引擎（双层结构，借鉴 kubeagent）

规则库分两层（对应 kubeagent 的体系 A/B，见 §4）：

**A 层：运行时故障根因 Detector（硬编码 Go）**

```go
// Detector 对单个 Pod 的事实做确定性根因判定。nil = "不是我的问题"。
// 一故障模式一文件；注册表注入时钟保确定性（Fuzz/golden test 依赖）。
type Detector interface {
    ID() string
    Detect(facts PodFacts) *Finding   // 纯函数，无 IO；now 由注册表注入
}
func Run(detectors []Detector, facts []PodFacts) []Finding // detector × pod 扇出
```
- MVP Detector 集（对齐 kubeagent 11 个 pod detector 的核心子集）：`OOM_KILLED`、`CRASH_LOOP`、`PENDING_UNSCHEDULABLE`（Unschedulable，含调度器 message）、`PROBE_FAILING`、`IMAGE_PULL_ERROR`、`RESTART_LOOP`、`CONTAINER_START_ERROR`、`CONFIG_ERROR`。
- "调度约束失配"细拆（资源不足/污点/亲和性）kubeagent 未做，aegis 需自研补强——列入后续，MVP 先对齐其 Unschedulable 粒度。

**B 层：结构缺陷 lint 规则（数据驱动 YAML DSL，抗 R17 主载体）**

```yaml
# internal/scan/policies/reliability.yaml（示例，规则是数据不是代码，可热更可评审可 eval）
- id: reliability.no-memory-limit
  match: {kind: Deployment, namespaces: ["*"]}
  assert: {path: "spec.template.spec.containers[*].resources.limits.memory", op: exists}
  level: warning
  message: "容器未设 memory limit"
```
- DSL schema：`{id, match:{kind,namespaces,labels}, assert:{path+op+values | relation}, level, message}`。
- **封闭操作集**（exists/notExists/in/notIn/matches/notMatches/gt/gte/lt/lte，数值比较支持 K8s quantity）——封闭是故意的，避免表达式语言成为第二个要 fuzz/sandbox 的面。
- **热修改（关键能力）**：规则是数据不是代码。引擎在每次 `Scan` 前重载规则目录（或经 fsnotify 监听变更失效缓存），**改 YAML 即生效、无需重编译/重启**；加载失败（校验不过）则保留上一份已知良好规则集并告警（fail-safe：绝不带病规则运行，也绝不因一条坏规则停摆）。规则覆盖集因此可在气隙/生产环境就地热调，这正是"减少 AI 审核压力"的运维面——规则迭代不依赖发版。
- **加载即 fail-fast 强校验**：`UnmarshalStrict`、id 正则、跨文件重复 id 报错、kind 白名单（刻意排除 Secret、禁 ConfigMap 读 data）、assert 恰设其一、证据脱敏截断。
- 被拒读的 kind 记入 `notEvaluated`/盲点，**绝不当 pass**。

**引擎**（对 A/B 两层统一编排）：

```go
type Engine struct { detectors []Detector; policyRules []PolicyRule; sanitizer Sanitizer; now func() time.Time }
func (e *Engine) Scan(s Snapshot) Result // A 层 Run + B 层 Evaluate → 聚合 → 消毒 → 确定性排序
```
- 新增 A 层故障模式 = 一个 `.go` 文件 + 注册表一行；新增 B 层规则 = 一个 YAML 条目。**均不改引擎**（数据驱动可扩展，PRD 判据）。
- `Result` 含 findings + `PartialReads`（盲点清单）+ coverage；排序确定性（供审计/回放/与 kubeagent 交叉验证）。

### 3.4 报告与脱敏（I4 / §4.2.2，借鉴 kubeagent 输出三件套）

- `Report` 渲染器输出 **JSON（版本化 scan-v1）+ SARIF 2.1.0**；HTML 单文件零 JS 作可选增强（对齐 kubeagent：无集群身份/apiserver 地址/kubeconfig 路径，可离线打开、可挂工单）。
- SARIF：合成 `k8s://<ns>/<Kind>/<name>` URI；severity 映射 Critical→error / Warning→warning / Info→note；**只声明实际触发的 rule**；有未豁免盲点时 `executionSuccessful=false`。
- 脱敏：报告**默认不含**集群身份、节点内网 IP、secret/env/镜像仓库路径等（数据最小化）；evidence 截断；审计只记安全展示值。
- 若 Finding 进入 LLM 上下文，整体经 `internal/sanitize` 的 `<untrusted_cluster_data>` 包裹（M1 形态 T1.5）。

## 4. 复用路线（kubeagent 优先，自研降级 — R17）【已定案：纯自研】

> 依据：`research/reuse-decision.md`（2026-10-08，kubeagent 源码一手调研）+ 决策者裁决（2026-10-08）。

**结论：纯自研 `internal/scan`，不依赖 kubeagent 任何形态（既不 import 代码，也不跑其 mcp 件）。仅借鉴其已验证的架构骨架与规则形态作为设计蓝本。**

| 路线 | 结论 | 关键理由 |
| --- | --- | --- |
| (a) import kubeagent 内核 | **排除** | 其全部功能代码在 `internal/` 下（80+ 包），是刻意"无公共库 API"设计；Go 编译期禁止外部 module import `internal/`。vendor/fork 继承全部规则腐烂长尾（R17），违背复用减负初衷 |
| (b) 跑 `kubeagent mcp` 薄封装 | **放弃** | 决策者裁决：对 kubeagent 的运行时强依赖不必要——其 `internal/` 封闭设计本身就是"不可复用"的强信号，押外部件引入供应链/维护不确定性，与 I11 气隙/私域零依赖姿态相悖。**不引入，连交叉验证基线也不依赖它** |
| (c) 借鉴设计自研 | **采用（唯一路线）** | scanner 须同进程、自控字段、可热更规则，原生接 aegis 闸门/DecisionRecord/eval；kubeagent 仅作设计蓝本（其架构与双层规则形态已被验证），不形成任何依赖 |

**决策备注（2026-10-08）**：原定"自研为主体 + kubeagent mcp 作交叉验证基线"中的基线依赖一并砍掉。理由：① 其 internal 封闭设计预示维护风险；② 气隙/私域场景要求零外部运行时依赖；③ 交叉验证改用**自研 golden 场景库 + fixtures**（eval 场景库第 1 号场景即 OOMKilled，见落地方案 T1.10），正确性由 eval 门禁与回归保证，而非外部件对照。

**自研须照抄的成熟模式**（kubeagent 一手源码证据，仅作设计蓝本，详见 `research/kubeagent-rules.md`/`kubeagent-mechanisms.md`）：

**自研须照抄的成熟模式**（kubeagent 一手源码证据，详见 `research/kubeagent-rules.md`/`kubeagent-mechanisms.md`）：

1. **双层规则库**（核心，抗 R17）：
   - **A 层 硬编码 Go Detector**（运行时故障根因）：`Detect(PodFacts) *Finding`（nil=不命中），一故障模式一文件，注册表 `DefaultDetectors(now)` 依赖注入时钟保确定性。对应本项目 Tier-0 的 CrashLoop/OOM/Pending/探针失败判定。
   - **B 层 数据驱动 YAML 规则 DSL**（结构缺陷 lint）：`{id, match, assert, level, message}` + **封闭操作集**（exists/notExists/in/notIn/matches/notMatches/gt/gte/lt/lte）+ 关系断言 + 加载即 fail-fast 强校验 + Secret/ConfigMap-data 排除 + 证据脱敏截断。规则是**数据不是代码**——可热更、可评审、可 eval，是抗规则腐烂的主载体。
2. **两阶段有界并发只读采集**（默认 8 worker，错误按索引序返回保确定性）。
3. **部分失败记"盲点"**（PartialReads）：RBAC 被拒读等记为盲点，**绝不静默当空**（避免"看着绿其实瞎"）。
4. **确定性排序**（findings.Flatten）：输出排序稳定，供审计/回放/交叉验证。
5. **置信度模型**：直接读到的 K8s 状态 = high；启发式 = medium；统计推断归因 = medium。
6. **脱敏报告**：HTML 单文件零 JS 无集群身份 / 版本化 JSON / SARIF（`k8s://<ns>/<Kind>/<name>` URI，severity 映射 error/warning/note）；审计只记安全展示值，绝不记 spec/env/secret。

**正确性保障（替代外部件交叉验证）**：自研规则库的正确性由 **golden 场景库 + fixtures + eval 回归门禁**（I8）保证——M1 验收的 OOMKilled 场景即 eval 场景库第 1 号（落地方案 T1.10），规则层解决率入 M3 eval 一等指标（§4.6/R17）作腐烂信号。不依赖任何外部件对照。

## 5. 接入与消费形态（M1）

1. **`aegis-cli scan`**：直接构造 adapter → Engine，输出报告。M1 只读 copilot 的离线演示入口（验收：断网/无 LLM key 下产出 OOMKilled 根因）。
2. **MCP 只读工具 `scan_cluster`**：经 `internal/mcpserver` 框架注册，`Metadata` 用 `roMeta`（RiskR0、幂等、超时上界）；Handler = 取快照 → 引擎 → 报告；结果由框架中间件统一包裹/限流（复用 T1.1 框架与错误协议）。是否独立成新 `mcp-servers/scan` 或并入 `k8s-read`，实现时按 depguard 与复用度定。

两条形态共用同一 `Engine`，差异仅在取数与封装。

## 6. 失效与边界

- **只读**：本组件永不触达写能力；`Suggestion` 仅为提示，不触发任何动作。
- **离线**：无任何外呼；无 LLM API key 时行为不变（I11 验收口径）。
- **降级**：apiserver 不可达时返回明确错误，不产出"半份"误报 finding；取数失败 ≠ 无异常。
- **盲点不静默**：采集中被 RBAC 拒读/部分失败的类别记入 `Result.PartialReads`，报告与 SARIF 显式渲染盲点，绝不静默当作"无异常"（借鉴 kubeagent，防"看着绿其实瞎"）。
- **不进热路径**（I2）：仅按需调用（CLI/工具），不做常驻 watch。

## 7. 测试策略

- **引擎纯逻辑**：表驱动单测，注入构造 `Snapshot`；每规则 ≥ 命中 / 不命中 / 边界三类用例。
- **适配层**：用 `reader.NewFromInterfaces` 注入 fake clientset，验证 Snapshot 聚合正确性。
- **MCP 形态**：复用 T1.1 黑盒/白盒测试模式（未知工具、超限、schema 契约、超时）。
- **脱敏**：断言报告不含预设敏感字段（种子 secret 值/内网 IP/镜像仓库路径）。
- **回归（M3 前置）**：finding 结构稳定，便于统计"规则层解决率"。

## 8. 落地方案补记

在《落地方案 v1.0》§4 M1 表补记 **T1.11**（规则层 scanner）一行，并标注与既有 `T1.1`（工具框架）/`T1.4`（双预算）的编号冲突说明，避免歧义。估时对齐设计文档 §8.1"确定性扫描层"：复用路线 10–20h；自研降级 +40–60h。
