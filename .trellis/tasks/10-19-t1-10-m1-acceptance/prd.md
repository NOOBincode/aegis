# T1.10 M1 OOMKilled 场景与联调验收

## Goal

eval 场景库第 1 号：OOMKilled 故障 manifest + 期望根因标注（格式即 M3 沿用）；演示脚本（注入→scan_cluster/Agent→报告）；M1 验收口径两项：根因命中 + replay。依赖 T1.3/T1.4/T1.6/T1.8。验收：kind 注入 OOMKilled，scan_cluster 命中 OOM_KILLED；Agent 诊断输出命中标注；aegis-cli replay 可回放。

## Requirements

1. `eval/scenarios/` 第 1 号场景：OOMKilled 故障 manifest + 期望根因标注（root_cause 含 OOM_KILLED；不依赖 LLM）。
2. 演示脚本：注入 → `scan_cluster` 命中 OOM_KILLED → Agent 诊断（LLM 通道就绪后）→ 报告；可清理。
3. M1 验收两项：①根因报告命中标注；②`aegis-cli replay` 回放完整决策链。
4. 依赖 T1.3/T1.4/T1.6/T1.8；LLM 端点未就绪时，scan/replay 部分仍必须可演示。

## Acceptance Criteria

- [x] 场景 manifest 可 apply/cleanup，重复运行幂等（`scripts/m1-demo.sh` 默认清理 ns）。
- [x] `scan_cluster` 命中 `OOM_KILLED` 且带 Pod 名；`partial_reads` 为空或显式说明。
- [x] LLM 就绪后 Agent 输出根因命中标注；`aegis-cli replay` 可回放同一会话（2026-10-19 实机，7 tool calls + outcome）。
- [x] 演示脚本在 kind 上从头跑通并留执行记录（`scripts/m1-demo.sh` full 模式 rc=0；artifacts 见 `.cache/m1-demo/`）。

## Notes

- 2026-10-18 起步：`eval/scenarios/oom-killed/` 场景已落盘并实机验证——busybox awk 分配触发
  cgroup OOM（exit 137/reason Error），scan_cluster 命中 OOM_KILLED（137 推断链，Medium）。
  剩余：Agent 报告（LLM 通道）、DecisionRecord/replay（T1.8）。

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Evidence

- 2026-10-18：Agent 端到端（DeepSeek 临时通道）OOM 诊断通过；日志注入/凭据提取/提权/数据外传测试均拒绝。详见 `evidence.md`（不含 API key；临时 Secret 已删除）。
- 2026-10-19：`scripts/m1-demo.sh` full 模式通过——scan 命中 OOM_KILLED；Agent 输出 OOM 根因 + 日志明文凭据风险；MCP audit 7 calls；`aegis-cli replay` 输出 `cluster_snapshot_ref` + 工具序列 + outcome；DecisionRecord apply 成功。真实测试 key 仅经临时 Secret，测试后 `ai-down` 删除；仓库/审计日志/远端无 key。
