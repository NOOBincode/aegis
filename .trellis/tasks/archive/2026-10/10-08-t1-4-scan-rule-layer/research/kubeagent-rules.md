# Research: kubeagent 规则库表示形式 / 粒度 / 故障模式覆盖

- **Query**: kubeagent 的规则是硬编码 Go 逻辑、数据驱动 DSL 还是 YAML？粒度与组织方式？覆盖哪些故障模式？
- **Scope**: external（源码 `internal/diagnose`、`internal/policy`、`internal/policypack`、`internal/knownissues`、各 `*health` 包）
- **Date**: 2026-10-08

## Findings

kubeagent **同时存在两套互补的"规则"机制**，用途不同，这正是对本项目 Tier-0 scanner 最有价值的发现：

| 体系 | 表示形式 | 面向 | 时机 |
|---|---|---|---|
| **A. Detector（诊断内核）** | **硬编码 Go**，一个故障模式一个文件/struct | 运行时**已发生**的故障根因判定 | 事后（post-failure） |
| **B. Policy/Pack（策略引擎）** | **数据驱动 YAML DSL**，一条规则一个 YAML 条目 | 上线**前**的静态结构缺陷 lint | 事前（pre-deploy gate） |

设计文档 §4.2.2 scanner 的描述（"CrashLoop/OOM/Pending/探针失败/调度约束失配等高频故障模式以规则库判定"）对应的是 **体系 A**；§8.1 R17 担心的"规则腐烂长尾"两种都中招。

---

### A. 硬编码 Go Detector（诊断内核，对应 Tier-0）

核心抽象极简（`internal/diagnose/diagnose.go`）：

```go
type PodFacts struct { Pod *corev1.Pod; Events []corev1.Event }
type Detector interface { Detect(facts PodFacts) *Finding }   // nil = "不是我的问题"
func Run(detectors []Detector, facts []PodFacts) []Finding     // 每个 detector × 每个 pod
```

- `Finding{Pod, Kind, Issue, Reason, Evidence, Resources, Container, Image, Confidence, LogCause, Suggestion}`。
- 注册表是 `DefaultDetectors(now)`（`internal/diagnose/defaults.go`），**硬编码 11 个 detector 的顺序切片**；`now` 依赖注入时钟保证纯函数/确定性（Fuzz + golden test 依赖）。
- **粒度**：一个故障模式 = 一个 `.go` 文件 + 一个实现 `Detect` 的 struct（几十行）。新增故障模式 = 一个新文件 + 注册表加一行，`Run` 循环不变（design.md line 108-113 明确这是扩展点）。

11 个 pod 级 detector（`internal/diagnose/`，非测试文件）及判定依据：

| Detector（文件） | Issue | 判定信号 | 置信度 |
|---|---|---|---|
| `crashloop.go` | CrashLoopBackOff | `State.Waiting.Reason=="CrashLoopBackOff"`；富化 restartCount/上次退出码/距今 | high |
| `imagepull.go` | ImagePullBackOff / ErrImagePull | `Waiting.Reason` ∈ 两者；记录 `Image`（不序列化，供归因） | high |
| `oomkilled.go` | OOMKilled | `State/LastTerminationState.Terminated.Reason=="OOMKilled"`；附 requests/limits | high |
| `pending.go` | Unschedulable | `Phase==Pending` 且 `PodScheduled` cond `False/Unschedulable` | high |
| `volumeattach.go` | VolumeAttachError | `FailedAttachVolume` event（多因 RWO Multi-Attach） | high |
| `volumemount.go` | VolumeMountError | `FailedMount` event（多为 ConfigMap/Secret 卷源不存在） | high |
| `restartloop.go` | RestartLoop | Running 但 `RestartCount≥RestartThreshold(=3)` 且仍在 flap，非 OOM | medium |
| `initcontainer.go` | Init:{CrashLoop/ImagePull/OOM/...} | 遍历 `InitContainerStatuses` 复用同一套信号 | high |
| `probefailure.go` | ProbeFailure | Running 但未 Ready 且 readiness/liveness/startup probe 持续失败（385 行，最复杂） | medium |
| `configerror.go` | CreateContainerConfigError | kubelet 消息里缺失 ConfigMap/Secret/key | high |
| `containerstart.go` | ContainerStartError | waiting 族兜底（RunContainerError/CreateContainerError/StartError），注册表最后、不猜测原因 | medium |

> `internal/knownissues/entries.go` 为每个 detector Issue 配了"已知成因 + 处置建议"的目录（数据驱动、按 Kind 排序），是 detector 之上的解释层，本身不含判定逻辑。

**Pod 之外的运行时健康规则**也都是硬编码 Go，按 `*health` 包组织、各自暴露 `Assess(...) *Report`，由 `scan.Evaluate` 统一调用：`clusterhealth`(节点 NotReady/kubelet Lease 心跳过期)、`svchealth`(Service 无 ready endpoints 的根因)、`ingresshealth`(路由后端缺失/端口未暴露)、`pvchealth`(PVC Pending 结构根因)、`pvcreclaim`(reclaim=Delete 数据丢失风险)、`pdbhealth`(PDB 阻塞 drain)、`hpahealth`(HPA 无法伸缩)、`webhookhealth`(Fail 策略 webhook 后端缺失/超时≥15s)、`quotahealth`(ResourceQuota ≥90%)、`certhealth`(证书过期)、`dnshealth`(CoreDNS SERVFAIL 比例≥5%)、`controlplane`(/readyz)、`nodehealth`(kubelet /healthz)、`diskusage`(节点/PVC 磁盘≥0.80)、`termhealth`(Namespace/Pod/PVC 卡 Terminating>2min)、`rollouthealth`(RolloutStuck)、`batchhealth`(Job/CronJob 失败)、`createhealth`(FailedCreate: 配额/LimitRange/webhook 拒绝建 Pod)、`nodereserve`(kubelet 未预留内存)、`secscan`(PSS 安全姿态)、`netpolicy`(NetworkPolicy 提示)、`connectivity`(apiserver 不可达分类)、`credlint`(明文凭据 lint)。

**跨对象归因**（`internal/rootcause/`）：`Annotate`（宕机节点）、`AnnotatePVC`（坏 PVC）、`AnnotateRegistry`（≥2 个 workload 共享同一 registry 的拉取失败 → 判定 registry 故障，统计推断故置信度 medium）。`internal/confidence/` 统一打 high/medium。

**覆盖与"调度约束失配"的关系**：本项目要点名的"CrashLoop/OOM/Pending/探针失败/调度约束失配"全部覆盖。其中"调度约束失配"对应 `pending.go`(Unschedulable)——它只报"无节点可调度"+调度器 message，**不进一步判定是资源不足/污点/亲和性哪一类**；更细的调度约束拆解需自研补强。

---

### B. 数据驱动 YAML 策略引擎（policy / policypack）

这是**真正的"数据驱动规则库"**，形态对 Tier-0 scanner 的"规则库可维护性"（R17 防腐烂）极具参考价值：

- **DSL schema**（`internal/policy/policy.go`）：一条规则 = `{id, match:{kind,namespaces,labels,namespaceLabels}, assert:{path+op+values 或 relation}, level(critical/warning/info), message}`。
- **操作集是封闭的 10 个**：`exists, notExists, in, notIn, matches, notMatches, gt, gte, lt, lte`——注释明写"封闭是故意的：表达式语言是第二个要 fuzz/sandbox/version 的东西"（`policy.go` line 31-34）。`matches/notMatches` 用自研 glob（`internal/glob`），数值比较支持 K8s quantity（500m vs 1、8Gi vs 4Gi，`policy/op.go compareNumeric`）。
- **关系断言 2 个**：`hasPodDisruptionBudget`、`hasHorizontalPodAutoscaler`（跨资源断言）。
- **纯函数**：`policy.Evaluate(rules, Inputs) (violations, notEvaluated)` 无 client、无 I/O，调用方读集群后把 `map[kind][]unstructured` 喂进来（`policy.go` line 1-13）。被拒读的 kind 记为 `notEvaluated` **绝不当 pass**。
- **加载即 fail-fast 强校验**（`policy/load.go`）：`yaml.UnmarshalStrict`、rule id 正则 `^[A-Za-z0-9._-]+$`、跨文件重复 id 报错、kind 白名单（约 28 种，`selectableKinds`，**刻意排除 Secret**、禁止 ConfigMap 读 `data/binaryData` 以防泄密进报告/SARIF）、`assert` 必须恰设 `path` 或 `relation` 之一、操作元数校验、证据脱敏截断 120 字符、确定性排序。
- **内置三个 pack**（`internal/policypack/packs/*.yaml`，`//go:embed`，"pack 是数据不是代码"）：`reliability.yaml`（14 条：无 readiness/liveness 探针、无 memory limit、image 用 :latest、单副本无 PDB……）、`security.yaml`（23 条：privileged、提权、runAsRoot、hostPath/hostNetwork/hostPID/Seccomp 未设……）、`cost.yaml`（16 条：大 request、history limit、backoffLimit、HPA maxReplicas、大 PVC……）。规则 id 以 pack 名做命名空间防冲突。

### Related Specs
- `.trellis/spec/k8s-read/` —— 本项目只读族包规范
- 设计文档 §4.2.2 line 235（scanner）、§8.1 R17 line 504（规则腐烂）

## Caveats / Not Found
- Detector 的判定逻辑**全部是 Go 代码**，不可被外部以"规则文件"形式注入或热更新；要新增故障模式必须改代码重编译。若 aegis 想要"可热更新的规则库"，应借鉴的是**体系 B（YAML policy DSL）**而非体系 A。
- `probefailure.go` 385 行是唯一大体量 detector，其余多在 20–60 行；复杂度集中在"运行中但探针失败"的多分支。
- 未逐字读 `restartloop.go`/`initcontainer.go`/`configerror.go`/`containerstart.go` 及各 `*health` 包内部，其判定阈值（如 RestartThreshold=3、Terminating>2min、quota≥90%、SERVFAIL≥5%）来自 README 与 `defaults.go`/`scan.go` 引用，精确实现以对应文件为准。
