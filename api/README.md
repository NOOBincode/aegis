# api — K8s 契约层（CRD Go 类型定义）

> 所属层：控制面契约层（kubebuilder 惯例） ｜ 里程碑：M1（DecisionRecord）→ M2（ChangeRequest/GuardrailPolicy）→ M4 占位（SchedulingHint） ｜ 设计文档出处：§5 ｜ 关联不变量：I6（契约即审计载体）、I10（状态机外置的载体） ｜ 关联失败模式：F14（在途恢复靠 CR 契约承载）

## 1. 设计初衷

`api/` 存放四个 CRD 的 Go 类型定义（ChangeRequest / GuardrailPolicy / DecisionRecord / SchedulingHint），它是全仓唯一"跨进程契约"的代码落点：controller、gatekeeper、CLI、eval 回放与各 mcp-servers 进程都要 import 它。把它独立成一层而非散在 `internal/` 里，理由有三：其一，契约的生命周期与业务逻辑不同——字段演进要遵守 K8s API 版本化纪律，混在业务包里会被顺手改坏；其二，依赖方向要求它处于最底端，任何层都不该为了拿类型而反向依赖业务包；其三，CRD 类型是"可评审的 YAML 的另一半"，把类型与校验标记集中在一处，PR 评审时契约 diff 一目了然。本层是 aegis 全部状态机（I10：状态外置）、审计载体（I6）与幂等语义（F15：idempotencyKey 字段先行）的地基。

## 2. 职责与任务清单

职责：

1. 定义 `aegis.dev/v1alpha1` 组的全部 CRD Go 类型与状态子资源。
2. 用 kubebuilder validation 标记表达字段级约束（必填、枚举、默认值、数值上下限），让 apiserver 替我们做第一道校验。
3. 提供 printer columns 标记，保证 `kubectl get` 可读。
4. 维护 `crds/` manifests 与类型的一致性（make 目标生成，CI 校验 diff）。
5. 执行版本演进纪律（v1alpha1 → v1beta1 的兼容规则），管理 conversion 接缝。

任务清单（落地方案 T 编号）：T1.8（DecisionRecord CRD，§5.3 字段全量）、T2.1（ChangeRequest/GuardrailPolicy CRD，§5.1/§5.2 字段第一版全量——含 abort/timeout/rollback.deadline/idempotencyKey，不留"以后再加"，对应 R6 整改）、M4 决策门通过后才追加 SchedulingHint 契约工作。W10 碎片任务含"CRD 字段对照 §5.1/§5.2 逐字段核对"。

明确不做：不放任何业务逻辑（见第 4 节负面清单）；不放 webhook 实现（准入强制点在状态机与工具协议层，见 `internal/controller/README.md`）。

## 3. 技术选型与开源包

- kubebuilder/controller-gen 惯例生成 deepcopy 与 CRD manifests；controller-runtime 的 scheme 注册。
- k8s.io/api、apimachinery 等核心库版本以 `deploy/versions.md` 钉死为准，与 client-go / controller-runtime 对账一致（aegis-go-quality 依赖纪律）。
- 每个 CRD 一个 types 文件 + 一个对应 `crds/*.yaml`；生成物不入手工编辑。
- 不引 kubebuilder 脚手架的工程模板本身（不需要 webhook/webhook-less 全套），只引其生成惯例；仓库布局以设计文档 §3.2 为准，不由 kubebuilder init 重建。

## 4. 具体设计（不写代码）

本层允许的净内容（白名单）：

1. 类型定义：spec/status 结构体与字段，字段名与设计文档 §5 yaml 字段名一一对应（驼峰对应连字符）。
2. 默认值与校验标记：kubebuilder validation（required/enum/minimum/maximum/default/pattern）；状态机的合法取值用枚举标记钉死。
3. deepcopy 生成方法（controller-gen 产物）。
4. printer columns 标记与 group/version 常量。

负面清单（PR 直接打回）：

- 任何业务逻辑：分级、策略、审批条件、回滚判定、归因——一律在 `internal/` 对应上下文。
- 任何 client-go 调用、外部系统访问、fmt 级的手工序列化 hack。
- 跨类型的"便利方法"若含行为（如"自动算级别"）即属越界；纯取值/判空级别的访问器可接受，但优先让调用方直接读字段，保持类型愚蠢。

版本演进策略：

- v1alpha1 即按"第一版即完整"标准设计（abort/恢复/超时/回滚字段随首版入库，设计文档 §5 开篇原则）。
- v1alpha1 期间字段只增不改语义：新增字段必须 optional 且带默认值，不删字段、不改类型、不收紧已放松的校验。
- 升 v1beta1 的触发条件：字段语义需要破坏性调整，或 M3 验收后契约冻结需求出现；届时 conversion  webhook/双版本存储策略另立 ADR（N3 非目标下不预设 HA 级灰度，附录 C 的 CRD 版本灰度不一致问题留档给姊妹篇）。
- 契约变更 = 独立 PR，附设计文档 §5 对照说明；`crds/` manifest 与类型同 PR 更新，CI 校验生成物新鲜度。

## 5. 错误处理与失效语义

本层无运行时进程，失效语义体现在"契约层如何把错误挡在 apiserver"：

| 错误类别 | 检测手段 | 处置策略 | 姿态 |
| --- | --- | --- | --- |
| 非法字段值（越界枚举/超配额数值） | kubebuilder validation 枚举与数值上下限标记 | apiserver 拒绝写入，错误返回提交方；业务层不兜底重复校验 | fail-closed（入口即拒） |
| 幂等键缺失/非法 | `idempotencyKey` 必填标记 + 长度/字符约束 | apiserver 拒绝；缺失幂等键的写意图连 CR 都建不出来（F15 的前置闸） | fail-closed |
| 契约与实现漂移（字段加了逻辑没消费） | CI 校验 `crds/` 由类型生成的 diff 为空；W10 逐字段对照检查 | 漂移即 PR 打回 | 机检 |
| 状态机非法取值 | status.state 枚举标记 + controller 侧白名单迁移表双保险 | apiserver 挡第一道，controller 拒未知状态并告警（F14 防线之一） | fail-closed |
| 版本灰度不一致（未来多版本期） | 留档附录 C，本期单版本不存在该失效面 | 姊妹篇处理 | 本期 N/A |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

本层不产生运行时遥测，但承载全部审计对象的 schema（I6 的载体）：

- DecisionRecord 的 `spec` 定义了审计落盘字段全集（输入快照引用、LLM 调用链引用、工具调用序列、消毒事件、关联 CR、结果、保留期）——回放与审计字段口径以本层类型为唯一事实源，见 `api/v1alpha1/README.md`。
- ChangeRequest 的 status 字段（state/executedOps/verification/rollback/abort/auditRef）是 F14 巡检、在途恢复与审计留痕的读取面。
- 指标/trace 不在本层产生；消费方（controller/gatekeeper）的指标命名见 `internal/controller/README.md` 与 `internal/README.md`。
- 契约本身的变更留痕走 Git 历史 + 独立 PR 纪律（第 4 节），不另设审计流。

## 7. 依赖方向与模块边界

谁调我：`cmd/*`、`internal/controller`、`internal/adapters/k8s`、以及任何需要引用 CR 类型的进程（含未来 gatekeeper 内嵌消费方）。

我调谁：仅 `k8s.io/api` / `apimachinery` 的元类型与 kubebuilder 标记依赖（metav1 等）。

禁止依赖谁：

- 禁止 import `internal/` 任何包、OPA、MCP SDK（契约层是依赖金字塔的最底端，谁都不许让它抬头）。
- 反向校验：任何业务包不得自带 CR 结构体副本，全仓只有一套类型定义。
- depguard 视角：本层不受 `context-purity` 规则约束（允许 k8s.io import），但受"禁止向上依赖"的方向约束，规则随 `.golangci.yml` 一并维护。

## 8. 测试策略与红队用例

- 类型层无单测惯例，测试落在消费方；但需保证：CRD yaml 可被 `kubectl apply --dry-run=server` 接受（kind 冒烟，kind-smoke.yml 的 apply `crds/` 步骤即此）。
- 契约测试：每个字段的 validation 标记至少有"合法值通过 / 非法值被拒"的成对用例（由 envtest/apiserver dry-run 承载）；枚举全集与设计文档 §5 逐字对照。
- 生成物新鲜度：CI 校验 controller-gen 重新生成的 deepcopy 与 `crds/` 同 PR 内容一致。
- 红队视角：契约是注入面之一——`intent`、`args` 类自由文本字段必须保持"只存不传"语义（其内容进 LLM 上下文前必经 sanitize 包裹，I4）；PR 评审对任何新增自由文本字段追问其消费路径上的包裹点，该追问写进 PR 模板检查单。
