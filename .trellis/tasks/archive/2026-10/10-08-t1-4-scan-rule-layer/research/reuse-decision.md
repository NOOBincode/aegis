# Research: kubeagent 复用路线对比与推荐（a import / b MCP 薄封装 / c 借鉴自研）

- **Query**: 为 aegis M1 规则层（Tier-0 scanner，`internal/scan` 独立包）在三条复用路线间给出成本/风险/可维护性分析与推荐
- **Scope**: mixed（external 源码 + internal 设计文档 §4.1/§4.2.2/§8.1/R16/R17）
- **Date**: 2026-10-08

## 背景对齐

设计文档 v1.5 已写明（**本调研结论与之独立核实一致**）：
- §1.5 line 59：kubeagent 是"只读诊断+白名单修复"最近邻，"验证 M1 形态可行，护城河在闸门+归因+eval"。
- §4.1 line 212：`kubeagent mcp` 是 L1 只读族现成件候选，"spike 第一周验证可复用性"。
- §4.2.2 line 235：scanner 是 Tier-0 确定性巡检内核，"复用 kubeagent mcp 优先，自研规则库为降级路径"。
- §8.1 R16 line 503 / R17 line 504：补入 kubeagent 至对标清单；规则腐烂长尾→复用优先、自研降级、规则层解决率入 eval 作腐烂信号。
- §6 确定性扫描层 line 442：复用路线 10–20h；自研 +40–60h 且有维护长尾。

---

## 路线 (a)：直接 import 其诊断内核

- **可行性**：**不成立（硬阻塞）**。kubeagent 全部功能代码在 `internal/` 下（`internal/scan`、`internal/diagnose`、`internal/policy`、`internal/remediate`……），design.md line 73 明写这是故意的——"a tool that has **no public library API**"。Go 工具链禁止外部 module import `internal/` 包，aegis `import "github.com/imantaba/kubeagent/internal/scan"` 会在**编译期**报 `use of internal package ... not allowed`。唯一"import"路径是 **vendor/fork 进自己仓库**改 module path。
- **若强行 vendor/fork**：成本陡增——需整树拷贝 80+ `internal` 包、跟随上游演进、自行解决 go 1.26.4 工具链与 client-go v0.36.2 版本对齐；一旦 fork 就继承**全部规则腐烂长尾**（R17），且丧失"复用"的减负意义。
- **依赖**：`go.mod` 直接依赖 9 个（cobra、client-go/api/apimachinery、modelcontextprotocol/go-sdk、anthropic-sdk-go、jsonschema-go、golang.org/x/term、sigs.k8s.io/yaml），传递依赖约 50 个，**无沉重/冷门传递依赖**，这一点本身可接受——但 import 不可行使这点无关紧要。
- **结论**：**排除**（除非走 vendor/fork，那是路线 c 的变种且更贵）。License Apache-2.0 允许拷贝，但工程上不值。

## 路线 (b)：跑它的 `kubeagent mcp` server 做薄封装

- **形态**：以子进程/sidecar 跑 `kubeagent mcp`（stdio），aegis 侧做薄封装调 `kubeagent_triage`（= 内部 `scan.Evaluate`，见 `internal/mcp/triage.go` line 68）拿 verdict+findings+coverage，映射成 Tier-0 findings。
- **成本**：**低**（贴合 §6 line 442 的 10–20h 复用路线）。无需读其源码即可用，黑盒契约（MCP 工具 + 版本化 JSON schema）稳定（README line 560：从 1.0 起仅 MAJOR 破稳定面）。
- **风险**：
  - **进程边界**：多一个常驻子进程/容器，生命周期、崩溃重启、版本钉版都要管；气隙环境要能离线拿到其二进制/镜像（Release 有 SBOM+签名，利好供应链，§8.1 关注）。
  - **契约面窄**：MCP 只暴露 triage/inspect/advisory/list_contexts，**拿不到 `scan.Result` 全量结构化字段**，只拿到压平的 findings+coverage；`--fix`、`--security`、证书、kubelet/控制面/DNS 健康等**不走 triage**（triage.go line 81-90 明确 markSkipped），需走 CLI。
  - **schema/行为漂移**：findings 的 Level/Issue 拼写随版本演进（findings.go line 8-10 注释坦承 severity 模型多处 ad hoc）。
- **可维护性**：好——上游活跃（2026-10-06 push），跟随升级即可；薄封装层只承担"元数据标注/字段映射"。
- **与设计契合**：**正是 §4.1 设想的 L1 只读族现成件**；但注意 §1.5 警句——它验证可行性、不构成护城河，aegis 的护城河（闸门/归因/eval）必须自建。

## 路线 (c)：仅借鉴设计，自研 `internal/scan`

- **形态**：新建 aegis `internal/scan` 独立包，**照抄其被验证的架构骨架**，规则按 aegis 需求自研。
- **成本**：**中高**（§6 line 442：+40–60h 且有维护长尾）。但这正是 R17 明确预留的"降级路径"。
- **可直接借鉴的成熟模式**（一手源码证据）：
  1. `Detector` 接口 + `Run` 扇出（`internal/diagnose/diagnose.go`）：`Detect(PodFacts)*Finding`，nil=不命中；新增故障模式=一个文件+注册表一行。
  2. 注册表注入时钟保确定性（`defaults.go`）：`DefaultDetectors(now)` 纯函数，Fuzz+golden test 依赖。
  3. **数据驱动 YAML 规则 DSL**（`internal/policy`+`policypack`）：`{id,match,assert,level,message}` + 封闭 10 操作 + 2 关系断言 + fail-fast 强校验 + Secret/ConfigMap-data 排除 + 证据脱敏截断 + 确定性排序——这是**抗规则腐烂**的关键形态（规则是数据不是代码，可热更/可评审/可 eval）。
  4. 两阶段有界并发只读采集 + 索引序错误返回（`scan.go`+`workers.go`+`parallel.go`，默认 8 worker，`KUBEAGENT_SCAN_WORKERS`）。
  5. 部分失败降级为"盲点" `PartialReads`，绝不静默当空（避免"看着绿其实瞎"）。
  6. 严重度模型（`findings.go`：Info<Warning<Critical）+ `Flatten` 投影 + 三种脱敏输出（HTML 单文件零 JS 无集群身份 / 版本化 JSON / SARIF `k8s://` URI）。
  7. 置信度 high/medium + `rootcause` 归因（node/PVC/registry）。
- **风险**：规则腐烂长尾（R17 已点名）；调度约束失配等 kubeagent 未细拆的故障模式需自行建模。
- **可维护性**：完全自主，规则库归 aegis 所有、随 aegis 演进，可直接接 aegis 闸门/DecisionRecord/eval。

---

## 对比表

| 维度 | (a) import 内核 | (b) MCP 薄封装 | (c) 借鉴自研 |
|---|---|---|---|
| 可行性 | **编译期阻塞**（internal/） | 高 | 高 |
| 一次性成本 | vendor 极高 | **低（10–20h）** | 中高（+40–60h） |
| 维护长尾 | 继承全部腐烂 | 低（上游维护） | 自扛（R17 腐烂风险） |
| 契约/字段完整度 | 全量（若可 import） | **窄**（仅压平 findings，无 fix/security/部分健康项） | 全量自控 |
| 进程/部署 | 无 | 多一子进程/sidecar | 无（同进程） |
| 气隙/供应链 | — | 需离线分发其二进制（有 SBOM+签名） | 无外部运行时依赖 |
| 与闸门/归因/eval 集成 | 需适配 | 弱（黑盒） | **原生** |
| License | Apache-2.0 可拷贝但工程不值 | Apache-2.0 | 借鉴无版权问题 |

## 推荐

**组合：主体走 (c) 自研 `internal/scan`（照抄其架构骨架与 YAML 规则 DSL 形态），并以 (b) `kubeagent mcp` 作 L1 只读族运行时装件 / 短期 bootstrap 与交叉验证基线。放弃 (a)。**

理由：
1. **(a) 硬阻塞**——internal/ 不可 import，vendor/fork 违背 R17"复用减负"初衷，排除。
2. **(b) 只能当"现成件"不能当"内核"**：契约窄（triage 不覆盖 fix/security/多项健康检查、只给压平 findings）、进程边界与气隙分发是真实成本，且护城河在闸门+归因+eval（§1.5），把 Tier-0 内核押在黑盒进程上不利于与 aegis 闸门/DecisionRecord 原生集成。**但**它作为 L1 只读族 MCP 现成件与"复用优先"降级路径的短期落地是对的，且可用作自研 scanner 的**交叉验证基线**（同一集群两路跑，比对 findings 一致率）。
3. **(c) 才是 Tier-0 scanner 的本体**：scanner 要"离线零 LLM 零 token、产出结构化 findings 与脱敏报告、接闸门与 eval"，必须同进程、自控字段、可热更规则。kubeagent 最大可复用资产不是它的二进制，而是它**已被验证的两层规则形态**——硬编码 `Detector`（运行时故障根因）+ 数据驱动 YAML policy（结构缺陷 lint）。aegis 应自研 `internal/scan` 同时实现这两层，YAML 规则库作抗 R17 腐烂的主载体。

> 一句话：**"复用 kubeagent"对本项目应落地为"复用其 MCP 件 + 复用其设计"，而非"复用其代码 import"**。scanner 内核自研、规则库用 YAML DSL 防腐烂、kubeagent mcp 作只读族装件与基线对照。

## 一手来源
- 仓库与 license：https://github.com/imantaba/kubeagent 、 https://api.github.com/repos/imantaba/kubeagent （Apache-2.0，2026-10-08 核实）
- internal 不可 import 的设计意图：`docs/design.md` line 73；`internal/diagnose/diagnose.go`（Detector/Run）
- 注册表/确定性：`internal/diagnose/defaults.go`
- YAML 规则 DSL：`internal/policy/{policy,op,load,eval}.go`、`internal/policypack/packs/*.yaml`
- 数据流/并发/盲点：`internal/scan/scan.go`、`internal/scan/workers.go`、`internal/parallel/parallel.go`
- 写安全机制：`internal/remediate/remediate.go`、`internal/audit/audit.go`、`internal/redact/redact.go`
- 输出：`internal/sarif/sarif.go`、`internal/htmlreport/htmlreport.go`、`internal/findings/findings.go`
- MCP 面：`internal/mcp/server.go`、`internal/mcp/triage.go`
- 内部对齐：`AI原生K8s安全操作平台-技术方案设计文档 (1).md` §1.5/§4.1/§4.2.2/§4.2.4/§6/§8.1（I11/R16/R17）

## Caveats
- 成本数字（10–20h / +40–60h）引自设计文档 §6 line 442，非本调研独立测算。
- 未跑通 `kubeagent mcp` 实测其 MCP 字节级契约（无可用集群+网络受限只拿到源码）；路线 b 的契约窄判断基于 `triage.go` 源码，建议 spike 第一周按 §4.1 实测确认。
