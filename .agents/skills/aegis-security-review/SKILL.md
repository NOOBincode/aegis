---
name: aegis-security-review
description: aegis 安全红线审查规范（I1–I10 检查单、L2 包人审、红队检查点、新写工具准入）。Use when reviewing PRs, modifying gatekeeper/L2 packages, adding a write tool, changing system prompts, preparing a milestone demo, running red-team checks, or updating docs/threat-model.md.
---

# aegis 安全红线审查

本项目护城河在闸门与 eval（设计文档 §1.5），安全审查是准入口径而非建议。

## I1–I10 检查单（PR 模板逐项过）

| 不变量 | 验收方法 |
| --- | --- |
| I1 确定性闸门 | 放行/拒绝路径中 grep 不到 LLM 输出直接参与判断；risk 终判 = 查表与 LLM 初判取高 |
| I2 LLM 不进热路径 | reconcile / 熔断 / 配额代码路径无 LLM 调用 |
| I3 工具即权限边界 | 无"任意 kubectl"类万能工具；每个工具窄 schema + 元数据五字段齐全 |
| I4 读路径消毒 | 集群返回数据全部 `<untrusted_cluster_data>` 包裹；注入命中 → 衍生动作自动升 R2 |
| I5 可回滚 | 写操作必有逆操作且 dry-run 验证通过；无法生成 → 自动升 R2 |
| I6 可回放 | DecisionRecord 完整：输入快照 / 工具序列 / 审批 / 执行结果 |
| I7 爆炸半径 | GuardrailPolicy 配额在执行路径强制，超限拒绝 |
| I8 eval 先行 | prompt / 模型 / 工具链变更附回归报告（M3 起 CI 强制） |
| I9 先归因后行动 | 字段所有权检查前置；控制器所有的字段直写拒绝 |
| I10 失效自明 | fail-closed 测试（杀进程 / 断 apiserver）：写阻塞 + 告警，读降级且标注 |

## L2 人审纪律

`internal/risk` `policy` `approval` `rollback` `breaker` `authority` `sanitize` `audit` `session` `internal/controller` 中 AI 产出逐行人审（落地方案 R-2）；本 skill 触发的 review 必须输出逐文件结论，不接受"整体看着没问题"。

## 红队检查点（里程碑末尾固定动作，不压缩）

- **M1**：日志注入初测——日志夹带指令文本，验证包裹隔离 + 告警 + 不产生写意图。
- **M2（项目准入门槛）**：审批绕过（自写客户端 / `--yes` 类参数）、日志注入、越权（诱导 R3 操作变体）、重放（复制 MCP 信封）、会话预算绕过，全量过且整改完才进 M3。
- **M3**：held-out 场景集轮换（F10）+ 控制器干扰并发零误修复。

## 新写工具准入（每加一个过一遍）

1. 元数据五字段齐全：`risk_hint` / `idempotent` / `reversible` / `est_blast_radius` / `timeout_ms`；
2. 只接受 ChangeRequest ID，协议层拒绝自由参数；
3. 逆操作生成 + dry-run 验证路径存在；
4. 红队用例至少 3 个：参数注入 / 越权目标 / 幂等重放；
5. `docs/threat-model.md` 同步更新攻击面。

## 审查输出模板

1. 结论：放行 / 打回；
2. 逐文件发现（严重度 P0/P1/P2，对应设计文档 §10 红队记录口径）；
3. I1–I10 影响面；
4. 需要补的红队用例；
5. 威胁模型更新项。
