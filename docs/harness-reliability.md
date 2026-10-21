# aegis-harness：意图日志与操作状态机（项目适配版）

> 状态：**proposed / design sync**，尚未实现；不改变当前 M2 已通过的安全结论。
> 来源：用户提供的《aegis-harness：意图日志与操作状态机 补充设计文档》v1.0（2026-09-17，参考 unreal-agent 思想）。
> 本文不是对来源文档的逐字复刻，而是**按 aegis 当前代码事实修正后的可执行版本**；冲突处以本文与代码为准，并保留差异说明。
> 关联：I6（可回放）、I10（失效自明）、主线 §4.2.5、`docs/test-matrix` skill；实现排期见 H1–H3，不阻塞当前 M2。

## 1. 当前事实（2026-10-21）

| 组件 | 现状 | 与 harness 的差距 |
| --- | --- | --- |
| ChangeRequest CRD | `spec` 保存意图/操作/幂等键；`status.state` 由 controller 写 | state 枚举缺 `Unknown`；无 preimage/desired/after 字段 |
| gatekeeper | MCP `request_change`、策略、审批 HTTP、mTLS；审批服务写 `status.approval` | **与 controller 双写 status**，违反单权威源 |
| controller | client-go informer + 单 worker；`--enable-writes` 才执行 | 无 journal、无 per-cluster actor、无 leader election |
| ScaleExecutor | dry-run → **merge patch** → verify replicas → rollback | 非 SSA；无 `fieldManager`/冲突处理；无 preimage |
| 审批 | gatekeeper-approval Deployment，Secret volume，R2 实测通过 | 审批写入仍是 status 字段，不是独立决策对象 |
| 恢复 | `Executing/Verifying` 只做 verify；无 Unknown/冲突分支 | 不满足 H-I4/H-I6 |
| 测试 | L0/L1/L5 已接；L3/L4 未实现 | journal==execution、kill-recovery 尚无 harness |

**结论**：当前 M2 的“R1/R2 执行链”是**功能闭环**，但还不是 harness 可靠性闭环。H1–H3 是下一阶段（建议 M2.5–M3），不能拿 M2 的 E2E 绿替代。

## 2. 按项目事实修正的六个不变量

| # | 修正后的不变量 | 与来源文档的差异 |
| --- | --- | --- |
| H-I1 | **CRD `spec`（持久意图）+ `status`（相位/结果）是恢复唯一权威；journal 只追加、只用于审计/金样本，恢复流程不得读 journal** | 来源 §4.3 写“重放意图日志”，与 H-I1 自相矛盾；此处明确裁决 |
| H-I2 | 副作用前先持久化：CRD status 迁到 `Executing` + journal 追加并 fsync；任一失败 → 不执行 | 保留，但 journal 不做恢复权威 |
| H-I3 | 写操作契约以**声明式期望态**为主：SSA `fieldManager=aegis`、`force=false`；命令式操作白名单 + 预镜像 + 幂等键 | 当前 `ScaleExecutor` 用 merge patch，需迁移 |
| H-I4 | 无法确认结果 → `Unknown`，绝不假装成功、不自动重放 | 当前 CRD 没有 `Unknown` 状态，需新增 |
| H-I5 | 写串行（全局 1 个 in-flight）；读有界并行；429 尊重 Retry-After；单次输出截断 | 保留；controller 当前单 worker/单副本，但无 leader election |
| H-I6 | 恢复 = **读取 CRD spec/status + 对账实际集群**；不是读 journal 纯重放 | 修正来源 §4.3 的表述 |

## 3. 单权威源：不要双写 status

当前 `gatekeeper.ApprovalServer` 直接 `UpdateStatus` 写 `status.approval`，而 `controller.Reconciler` 整体写 `status`（含 `state/conditions/execution`）。这会产生 resourceVersion 冲突与“审批已写但 controller 覆盖”的窗口。

**决定（H-I1 前置）**：

1. `controller` 是 ChangeRequest `status` 的唯一 writer。
2. gatekeeper 审批端点不再写 ChangeRequest status；改为创建不可变 `ApprovalDecision`（或等价独立对象/事件），字段：
   ```yaml
   kind: ApprovalDecision
   spec:
     changeRequestRef: {namespace, name}
     approver: alice
     decision: approved|denied
     channel: cli|webhook
     decidedAt: ...
     evidenceHash: sha256:...
   ```
3. controller 消费 ApprovalDecision，写 `status.approval` 并推进状态机。
4. 迁移前，审批 API 保持**只读校验**，不写 status；否则 H-I1 不成立。

> 这是对现有 `cmd/gatekeeper/approval_server.go` 的明确修正项，不是可选优化。

## 4. 状态机映射

来源图的相位映射到当前 CRD：

| harness 相位 | 当前 `ChangeRequestState` | 说明 |
| --- | --- | --- |
| `DryRun` | `DryRunning` | 需先落盘 dry-run hash/preimage |
| `GateCheck` | `Pending`/`AwaitingApproval` | gatekeeper 已裁决；不要把“判定”和“执行”混在一个相位 |
| `AwaitingApproval` | `AwaitingApproval` | 只由 controller 依据 ApprovalDecision 写 |
| `Applying` | `Executing` | 执行前 status 先落盘 |
| `Verifying` | `Verifying` | 读回 observedGeneration/spec |
| `Recorded` | `Committed` | `after` 必须读回并持久化 |
| `Rejected` | `Rejected` | R3/策略拒绝不落执行 |
| `RollingBack` / `RolledBack` | `RolledBack` / `PartiallyRolledBack` | 回滚也是同一状态机的操作 |
| `Unknown` | **缺失（待增）** | H-I4 的硬要求 |
| `StopHard`/`StopWhenIdle` | `Aborted` 条件 | 需补 in-flight 等待语义 |

**必须新增 `Unknown` 状态**（CRD enum + controller transition + tests）。进入条件：

- apply 超时/API 不可达/watch 未确认；
- 执行结果无法与期望态对齐；
- 字段所有权冲突且无法安全判定。

离开条件仅两种：对账确认期望态 → `Committed`；确认需要回滚 → `RolledBack`/`PartiallyRolledBack`，并记录冲突证据。

## 5. 预镜像与声明式执行

当前 `ChangeOperation` 只有 `tool/args/risk/authority/inverse`。需要补齐：

```yaml
# 目标字段（示意）
before:
  resourceVersion: "12345"
  objectHash: sha256:...
  payload: {...}          # 已 sanitize，大小受限
desired:
  patchType: SSA
  fieldManager: aegis
  payload: {...}
after:
  resourceVersion: "..."
  objectHash: sha256:...
```

纪律：

- before/desired 在 apply 前落盘；after 在 verify 后落盘；
- SSA 固定 `fieldManager=aegis`，`force=false`；冲突 → `Unknown` + 人审/冲突分支，不偷偷抢字段；
- 回滚 = `apply(before)`，仍走同一状态机，不绕过 journal/审批；
- 无法期望态化的 delete/restart 等，白名单 + 预镜像 + 默认 R2；
- ScaleExecutor 应从 merge patch 迁移为 SSA；`envtest` 验证 field management（fake clientset 不足以验证 SSA）。

## 6. 意图日志（journal）

### 6.1 角色

- **只追加**、每行一个事件、带校验和；
- 位于 controller 的持久卷/审计 sink，而不是 emptyDir；
- 仅用于审计、金样本回放、故障取证；**恢复不读它**。

### 6.2 事件

保留来源的六类：`Input / Intent / Approval / OpTransition / Checkpoint / Result`（结果事件建议显式，不用“隐式结束”）。

每行最小字段：

```json
{"v":1,"seq":42,"type":"OpTransition","ts":"...","cr":"kagent/cr-...","requestID":"sha256:...","from":"Executing","to":"Unknown","reason":"watch timeout","checksum":"sha256:..."}
```

- `seq` 单调；`checksum` 覆盖上一行 checksum（hash chain）；
- `requestID` 与目标对象注解 `aegis.dev/request-id` 一致；
- 入日志前必须过 `internal/sanitize`/`internal/redact`，禁止 Secret/原始凭据；
- fsync 失败 → 视为事件未持久化，**不执行副作用**。

### 6.3 journal 与恢复

恢复流程固定为：

```text
读取 CRD spec + status
  → 读取 ApprovalDecision（如有）
  → 读取实际集群/目标对象（ownership + field manager）
  → 三分支：未生效→受限重试；半途→按 before 回滚；被他人改→Unknown/冲突升 R2
```

即“重放意图”应理解为**重读持久意图（CRD spec）**，不是回放 journal。来源 §4.3 的“重放日志”表述在本文作废。

## 7. 有界、心跳与 Stop

- 写：controller 全局单 in-flight；多副本前必须 leader election；
- 读：诊断工具可有界并行；模型在飞请求限制只约束写路径决策；
- API 429：按 `Retry-After` 退避，不把 429 当成功；
- 长 reconcile：heartbeat/条件时间戳；看门狗探活；
- `StopHard`：取消 pending，等待 in-flight 到终态（或 Unknown），再退出；
- `StopWhenIdle`：对账完成才退出；
- 当前 controller 无 probe/leader election，H2 必须补。

## 8. 与现有测试矩阵的关系

| 测试级别 | harness 验收 |
| --- | --- |
| L0 单测 | fake clientset + fake watch 注入 NotFound/Conflict/Timeout/429；断言失败后不推进 |
| L1 集成 | **envtest**（真 apiserver、真 SSA、真 watch）；fake clientset 不用于字段所有权断言 |
| L2 E2E | 完整 ChangeRequest 状态序列 + ApprovalDecision + DecisionRecord/audit 对账 |
| L3 回放/金样本 | journal==执行 + **规范化后字节一致**；golden 目录见 §9 |
| L4 故障注入 | kill -9 controller（dry-run 后/apply 后/verify 中）、断 apiserver、重启；零污染 + 三分支 |
| L5 | race + 性能；SSA 序列化稳定（map 键序） |
| L6 | 真模型只触发 request_change；审计完整 |
| L7 | 审批绕过/重放/自定 ownership 冲突/Unknown 诱导 |

**L3 的“字节一致”定义**：对事件做规范化（固定时间戳/随机 ID 为占位符、JSON 键排序、去掉调试验证字段）后，journal 与期望状态序列的 canonical bytes 必须一致；原始含动态 ID 的输出不能直接比较。

**L4 零污染定义**：测试 namespace/CR/finalizer/审批对象/PVC 临时文件全部清理；重复 idempotencyKey 不产生第二个副作用；Unknown 不被写成 Committed。

## 9. 金样本目录（目标）

```text
testdata/harness/
  oom-r1/
    journal.jsonl
    expected.json
  r2-approval/
    journal.jsonl
    expected.json
  r3-reject/
    journal.jsonl
    expected.json
  unknown-timeout/
    journal.jsonl
    expected.json
```

- 内容必须含 `v/seq/checksum` 链；
- `expected.json` 固定状态序列和最终对象 hash；
- 更新 golden 必须在 PR 说明“行为变化”并经 reviewer 确认，不能测试失败就静默重生成。

## 10. 里程碑（不阻塞 M2）

| 阶段 | 内容 | 退出标准 |
| --- | --- | --- |
| H1 | journal + checksum + fake 故障注入；不改恢复权威 | fake 下 journal==演进；L3 骨架可跑 |
| H2 | Unknown 状态 + preimage + SSA + envtest + kill-recovery | L1 envtest、L4 三分支、零污染 |
| H3 | ApprovalDecision 单写者 + controller actor + Stop/心跳 | 审批→执行全链路单一 status writer |

**当前状态（2026-10-21）**：H1 的 journal 纯领域库已落（append/fsync/checksum hash chain/Verify，单测通过）；H1 的 fake 故障注入、H2/H3 均未实现。M2 的 E2E 只证明功能链，不证明 harness 可靠性。

## 11. 开放决策（需要确认）

1. **单写者迁移**：ApprovalDecision 新 CRD，还是把审批并入 controller 进程？本文推荐前者（保持审批/执行进程边界）。
2. **journal 存储**：controller StatefulSet + PVC（kind local-path）还是外部 append-only audit sink？H-I2 的 fsync 要求使本地 PV 更直接，但需要备份/轮转。
3. **CRD 迁移**：`Unknown`、before/desired/after、requestID 注解是否一次加，还是分组迁移？本文建议一次设计、分组实现。
4. **envtest 可用性**：CI 是否接受下载 envtest 二进制；当前环境无 `/usr/local/kubebuilder`。
5. **多副本**：H2 前是否强制 replicas=1 并加 leader election 占位。

## 12. 与当前流程的同步

- 本文纳入 `docs/README.md` 档案层；
- `aegis-test-matrix` Skill 的 L3/L4 以本文为 oracle；
- Trellis 任务 `10-21-harness-reliability` 跟踪 H1–H3；
- PR 模板的 I6/I10 检查项引用本文，而不是把“journal 未实现”误报为已满足；
- 任何实现必须先回答 §11 的开放决策，不能边写边定。

## 修订历史

| 日期 | 版本 | 变更 |
| --- | --- | --- |
| 2026-10-21 | v0.1 | 基于用户提供的 harness 设计，按当前 gatekeeper/controller/CRD 事实修正；标记 H1–H3 未实现 |
