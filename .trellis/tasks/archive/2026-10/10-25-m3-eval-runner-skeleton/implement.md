# M3 eval runner 执行计划

1. 为 `eval/scenarios/oom-killed/expected.json` 增加 disposition。
2. 实现 `eval/runner.py`：场景模型、命令注入、六阶段、cleanup、证据采集、原子 run 归档。
3. 在 `eval/graders/` 实现确定性根因/对象匹配与四指标骨架。
4. 新增 `eval/tests/test_runner.py`，覆盖成功、失败、A2A 不泄题、误修复、MTTR 和归档。
5. 修改根 Makefile 增加 `test-eval`、`eval-smoke`，保持现有 CI workflow 不变。
6. 同步 `eval/README.md`、`eval/scenarios/README.md`、`Agent.md` 的运行方式和骨架边界。
7. 运行 `python3 -m unittest discover -s eval/tests -v`、`make test-eval`、Go lint/test-unit/build 和密钥检查；真实 smoke 只按环境事实记录。

回滚点：Python 与 Makefile 独立于 M2 Go 改动；若 runner 行为不清，先修单测和错误分类，不用人工报告替代真实执行。
