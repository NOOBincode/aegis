# T1.8 DecisionRecord + aegis-cli replay

## Goal

DecisionRecord CRD（§5.3 字段全量）+ internal/audit 组装 + aegis-cli replay 子命令；M1 OOMKilled 会话的输入快照/工具序列/结论可回放。验收：CRD apply；一次会话落 DecisionRecord；replay 输出完整决策链；spec 一次性写入 + sealed 语义（可 M2 加固）。

## Requirements

1. `api/v1alpha1` DecisionRecord 类型 + `crds/` CRD manifest（§5.3 字段全量：sessionRef/clusterSnapshotRef/llmTraceRef/toolCalls/sanitizationEvents/changeRequests/outcome/retentionDays）。
2. `internal/audit` 组装器：从会话工具调用序列生成 DecisionRecord；`aegis-cli replay` 读取 DecisionRecord 并输出完整决策链。
3. M1 阶段可先用文件/CRD 二选一落盘；spec 不可变与 sealed 语义按 §5.3 做最小实现。
4. 不得为了让 replay 好看而省略工具入参摘要/结果摘要/错误类别。

## Acceptance Criteria

- [ ] CRD apply 成功；一次 OOMKilled 会话落一份 DecisionRecord。
- [ ] `aegis-cli replay` 输出输入快照引用、工具调用序列、结论；缺字段时明确报错而非静默。
- [ ] 单测覆盖序列化/反序列化与缺字段路径；`make lint` 0 issues。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
