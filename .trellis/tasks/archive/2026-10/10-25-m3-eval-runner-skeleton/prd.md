# M3 eval runner 骨架

## Goal

以 Python 标准库交付 T3.1 最小 runner：场景校验、六阶段执行、确定性评分、run 归档与 unittest。

## Requirements

- 复用 `aegis-eval-scenario-v1`，补充明确的期望 disposition；不 import Go 包、不引入第三方 Python 依赖。
- 支持 scan 离线 Tier-0 与 a2a 只读驱动；a2a 提示不得携带期望根因。
- 用 ChangeRequest 前后差异证明真实写动作和 MTTR，不把文本建议当成执行。
- 四指标保留分母与 n/a；MTTR 只接受 Committed 证据。
- 归档 schema=`aegis-eval-run-v1`，原子写入；基础设施/注入/评分/cleanup 失败分类清楚，半份报告不得进基线。
- 不做 20 场景、完整语义 grader、CI 回归 workflow 或 M3 全量验收。

## Acceptance Criteria

- [x] `eval/runner.py`、`eval/graders/` 与 `eval/tests/test_runner.py` 可执行。
- [x] 覆盖有效加载、脏场景零执行、scan、a2a 不泄题、失败 cleanup、误修复、MTTR 与归档。
- [x] `python3 -m unittest discover -s eval/tests -v` 与 `make test-eval` 通过。
- [x] Makefile 有 `test-eval`、`eval-smoke`，eval/Agent 文档说明运行方式和骨架边界。
- [x] 真实 kind 故障只记环境阻断，不人工改成 pass。
