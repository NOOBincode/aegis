# T1.8 DecisionRecord + aegis-cli replay

## Goal

DecisionRecord CRD（§5.3 字段全量）+ internal/audit 组装 + aegis-cli replay 子命令；M1 OOMKilled 会话的输入快照/工具序列/结论可回放。验收：CRD apply；一次会话落 DecisionRecord；replay 输出完整决策链；spec 一次性写入 + sealed 语义（可 M2 加固）。

## Requirements

1. `api/v1alpha1` DecisionRecord 类型 + `crds/` CRD manifest（§5.3 字段全量：sessionRef/clusterSnapshotRef/llmTraceRef/toolCalls/sanitizationEvents/changeRequests/outcome/retentionDays）。
2. `internal/audit` 组装器：从会话工具调用序列生成 DecisionRecord；`aegis-cli replay` 读取 DecisionRecord 并输出完整决策链。
3. M1 阶段可先用文件/CRD 二选一落盘；spec 不可变与 sealed 语义按 §5.3 做最小实现。
4. 不得为了让 replay 好看而省略工具入参摘要/结果摘要/错误类别。

## Acceptance Criteria

- [x] CRD apply 成功；一次 OOMKilled 会话落一份 DecisionRecord（2026-10-19 实机：`crds/aegis.dev_decisionrecords.yaml`；replay YAML apply 到 kagent）。
- [x] `aegis-cli replay` 输出输入快照引用、工具调用序列、结论；缺字段时明确报错而非静默（`cluster_snapshot_ref` + `llm_trace_ref` + 7 tool calls + outcome）。
- [x] 单测覆盖序列化/反序列化与缺字段路径；`make lint` 0 issues。

## Evidence

- `api/v1alpha1/decisionrecord_types.go` + `crds/aegis.dev_decisionrecords.yaml`；`internal/audit`（纯 stdlib，JSONL 解析/聚合/渲染）；`cmd/aegis-cli replay`（text/yaml）。
- MCP 审计行新增 `session_id/args_preview/result_sha256/result_bytes/result_preview/redacted_count`；`scripts/m1-demo.sh` 采集 `kubectl logs` → replay → apply DecisionRecord。
- 2026-10-19 实机：`m1-demo.sh` full 模式（Agent+replay）通过，ToolCalls=7、clusterSnapshotRef=sha256:...；DecisionRecord apply 成功。
- 残余：`llmTraceRef` 为空（kagent/OTel trace 引用 M3 接）；spec 不可变/sealed 语义 M2 加固。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
