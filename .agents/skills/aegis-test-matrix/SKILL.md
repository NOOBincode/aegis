---
name: aegis-test-matrix
description: aegis 仓库的 L0–L7 测试/验证矩阵（单测、集成+fake LLM、自动化 E2E、金样本回放、故障注入恢复、race/性能、真模型 E2E、渗透测试）。Use when adding or changing tests, CI gates, Makefile test targets, milestone/release verification, regression/smoke/benchmark runs, fake-LLM harnesses, replay/golden fixtures, fault injection, race/performance baselines, real-model E2E, or penetration/red-team checks in the aegis repository.
---

# aegis L0–L7 测试矩阵

本仓库是“AI 操作 K8s 的闸门”，测试是安全证据，不是形式。每一级都必须有**命令、断言、证据文件、退出标准**；缺一项就不能宣称该级通过。

## 0. 总原则

1. **fail-closed**：测试环境/依赖/断言缺失时返回非零，不允许跳过或默认通过。
2. **每次提交 vs 合并前**：L0/L1 每次提交；L2/L3/L4/L5 合并前；L6 里程碑/发布前；L7 定期。
3. **假模型与真模型分离**：L1/L2/L3 用 fake LLM（确定性、零外泄）；L6 才允许真实 provider key。
4. **证据留存**：日志/JSON/JUnit/审计记录写入 `.cache/test-matrix/<level>/`（gitignore），报告只引用路径，不贴 key。
5. **密钥零泄露**：任何测试不得把真实 key 写进仓库、日志、PR、shell history；结束必须 `scripts/check-secrets.sh --history`。
6. **等级不能越级**：L2 绿不能替代 L0/L1；L6 绿不能替代 L3/L4。

## 1. 矩阵与门禁

| 级别 | 名称 | 触发 | 当前仓库入口 | 退出标准 |
| --- | --- | --- | --- | --- |
| L0 | 单元测试 | 每次提交 | `make test-unit` | 全 module 0 fail；断言未被弱化/删除 |
| L1 | 集成 + fake LLM | 每次提交 | `make test-integration` + fake-LLM 契约（待补） | 真依赖契约通过；fake LLM 请求/响应/审计断言通过 |
| L2 | 自动化 E2E（替身） | 合并前 | `make gatekeeper-up controller-up`、`make security-smoke`、`AEGIS_DEMO_WITH_AGENT=0 make m1-demo` | 完整用户路径字节级步骤走通；DecisionRecord/audit 流断言通过 |
| L3 | 回放/契约金样本 | 合并前 + 里程碑 | `aegis-cli replay` 对 golden 记录（待补） | 输出/决策与金样本**字节一致**；差异必须 fail |
| L4 | 故障注入 + 恢复 | 合并前 | 杀 gatekeeper/apiserver/controller、断网、OOM、重启（待补脚本） | 失败时 fail-closed；恢复后零残留 CR/资源/锁；审计可回放 |
| L5 | race + 性能 | 合并前 | `make test`（`-race`）、`make bench` | race 0；性能/分配不劣于确认基线 |
| L6 | 真模型 E2E | 里程碑/发布前 | `AEGIS_LLM_API_KEY=... scripts/m1-demo.sh` + Gatekeeper/Operator 路径 | 审计流完整（工具序列/审批/结果）；关键断言通过；key 结束即删 |
| L7 | 渗透测试 | 定期 + 里程碑 | `make security-smoke`、`make redteam-netpol` + 完整 M2 红队 | 无高危；中危有修复/接受记录；密钥零泄露 |

**合并门禁（M2 起）**：L0/L1/L2/L3/L4/L5 全绿才可合并；L6 在里程碑/对外演示前必须绿；L7 至少每个里程碑跑一次。当前 L1 fake-LLM、L3 golden、L4 fault-injection 仍是**未实现门禁**，不能以“测试脚本不存在”为由宣称通过。

## 2. 各级执行细节

### L0 单元测试

```bash
make test-unit
```

- 逐 module `go test ./...`，不启动集群、不读真实 key。
- 断言变更审查：删除/弱化测试等同于功能变更，必须在 PR 说明。
- L2 安全包（risk/policy/approval/rollback/breaker/authority/sanitize/audit/session/controller）覆盖率目标 ≥80%。

### L1 集成 + fake LLM

```bash
make test-integration
```

- 真依赖：kind 内 MCP server/Service/RBAC/网络策略。
- fake LLM：OpenAI 兼容替身，返回固定 tool-call/文本；
  - 断言：请求 payload（模型/消息/工具 schema）、超时、重试、审计关联；
  - 禁止访问公网模型。
- 当前缺口：fake-LLM 契约 harness 未落盘；在补齐前 L1 只能算“集成部分通过”，不能宣称 L1 绿。

### L2 自动化 E2E（替身）

```bash
make gatekeeper-up controller-up
make security-smoke
AEGIS_DEMO_WITH_AGENT=0 make m1-demo
```

- 使用替身/本地 gatekeeper，不用真实模型。
- 必断言：
  - `ChangeRequest` 状态 `Pending/AwaitingApproval/Executing/Committed` 正确；
  - R2 未审批不执行；R3 不落库；
  - DecisionRecord 的 `session/tool_calls/outcome` 与 audit JSONL 对账；
  - `aegis-cli replay` 可读。
- L2 绿不能替代 L6：L2 只证明路径/契约，不证明真模型行为。

### L3 回放/契约金样本

```bash
# 目标形态（待补 golden harness）
aegis-cli replay --record testdata/golden/<case>/decisionrecord.yaml --out /tmp/out.txt --format text
diff -u testdata/golden/<case>/expected.txt /tmp/out.txt
```

- 金样本必须固定：非确定性字段（时间/随机 session）在生成前 normalize，或作为占位符；
- **字节一致**：`diff` 非零即失败；
- 覆盖：OOM 场景、R2 审批、R3 拒绝、策略拒绝、MCP 信封重放。

当前缺口：golden 目录/harness 未建；在补齐前 L3 不是绿。

### L4 故障注入 + 恢复

目标注入（脚本待补）：

- kill gatekeeper / controller / apiserver 代理；
- 断开 gatekeeper→apiserver、controller→apiserver；
- 审批 Service 不可用；
- 执行中 Pod 被删/节点重启；
- 注入后重试同一 `idempotency_key`。

退出标准：

- 写操作在依赖不可用时**阻塞**，不允许降级放行；
- 恢复后不重复执行、不产生孤儿 CR/finalizer/临时资源；
- 审计记录能解释失败阶段与恢复动作；
- 清理后 `kubectl -n kagent get cr` 为空、测试 namespace 不存在。

### L5 race + 性能

```bash
make test          # -race（当前 test 目标）
make bench         # 关键热点
```

- race 0；并发用例 goleak 无泄漏。
- 基线：`benchstat` 对比上一次确认基线；`ns/op`/`B/op`/`allocs/op` 任一显著劣化（>10%）需要解释。
- 当前基准热点：`internal/securitytest`（risk/policy/gatekeeper/sanitize/redact/audit）、`internal/scan`。
- 不允许用 `-count=1` 掩盖抖动；至少 `-count=3`。

### L6 真模型 E2E

仅在里程碑/发布前、且用户明确提供 key 时：

```bash
AEGIS_LLM_API_KEY='<from secret manager, not shell history>' scripts/ai-up.sh --with-agent --restart
scripts/m1-demo.sh
# 结束后必须
scripts/ai-down.sh
```

- 断言：Agent 工具序列、DecisionRecord、audit JSONL、replay 输出、无直写；
- key 只经环境/Secret，不落仓库/日志/PR；
- 结束验证 `kubectl -n kagent get secret aegis-llm` 不存在、`check-secrets --history` 通过。

### L7 渗透测试

定期 + 里程碑：

```bash
make security-smoke
make redteam-netpol
# M2 红队矩阵（当前已有单测/实机片段）
```

- 覆盖：审批绕过、自批、重放、篡改、日志注入、R3 变体、越权目标、MCP 客户端伪造、网络隔离、预算绕过；
- 高危 = 0；中危必须有修复或书面接受记录；
- 测试证据只保留路径/摘要，不保存真实凭据。

## 3. 失败语义

| 失败 | 处理 |
| --- | --- |
| L0/L1 红 | 阻断提交/合并；修复，不弱化断言 |
| L2 审计断言失败 | 阻断合并；查 gatekeeper/DecisionRecord/audit 三方一致性 |
| L3 字节差异 | 阻断；确认是预期变更才更新 golden，并在 PR 说明 |
| L4 残留/重复执行 | 阻断；视为安全缺陷，必须修复 |
| L5 race/性能劣化 | race 立即阻断；性能需基线解释或修复 |
| L6 key 泄露/审计缺口 | 阻断发布；吊销 key |
| L7 高危 | 阻断里程碑；修复后重跑 |

## 4. 证据与报告

```text
.cache/test-matrix/L0/unit.log
.cache/test-matrix/L1/integration.log
.cache/test-matrix/L2/e2e.log
.cache/test-matrix/L3/replay-diff.txt
.cache/test-matrix/L4/fault-inject.log
.cache/test-matrix/L5/race.log / bench.txt
.cache/test-matrix/L6/real-model.log
.cache/test-matrix/L7/redteam.log
```

- PR/里程碑报告引用这些路径，不贴 key；
- 每次结束跑 `scripts/check-secrets.sh --history`；
- 报告必须写清“哪一级没跑/为什么”，不允许用 L0 绿代替全矩阵。

## 5. 当前实现状态（2026-10-21）

- ✅ L0：`make test-unit`（新增）
- ✅ L5：`make test`（-race）+ `make bench`（新增；基线待确认）
- ✅ L1 集成部分：`make test-integration`；fake LLM 未实现
- ✅ L2 可用命令：gatekeeper/controller/security-smoke/m1-demo（无 Agent）；审计断言脚本未完全自动
- ❌ L3 golden harness：未实现（oracle 与金样本目录见 `docs/harness-reliability.md`）
- ❌ L4 fault-injection harness：未实现（kill-recovery/零污染 oracle 见同文档）
- ✅ L6 手工流程：`scripts/m1-demo.sh`（需 key）
- ⚠️ L7：已有 security-smoke/redteam-netpol + 单测红队；完整 M2 渗透矩阵未闭环
- 结论：**L3/L4 未实现前，M2 不能宣称“全矩阵通过”**。

## 6. OpenSpec/Trellis 联动

- 测试相关变更创建 `test` 或 `ci` 类型 Trellis 任务，PR 描述附矩阵勾选；
- 新增测试脚本/门禁前先写清“守哪条不变量/失败模式”；
- 不把真实 key/生产集群放进任何级别的自动化。
