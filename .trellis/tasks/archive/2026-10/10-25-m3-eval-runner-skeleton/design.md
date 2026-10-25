# M3 eval runner 设计

## 边界

- `eval/` 是独立 Python 世界，只用标准库；Go 侧不 import eval，eval 不 import Go。
- command runner 可注入：生产 subprocess，测试 fake；命令以 argv 执行，不用 shell 拼接。

## 六阶段

1. load：校验 schema、id、manifest、inject/cleanup、根因/对象/disposition，拒绝仓库外 manifest。
2. setup：执行环境探针，失败记 infrastructure_failure。
3. inject：执行场景注入，记录 t0；失败仍 finally cleanup。
4. agent：scan 输出 JSON 或 a2a 输出文本；采集前后 ChangeRequest 名称/status/riskLevel。
5. grade：rule ID + 对象 + 根因锚点双维度判定，计算四指标与分母/n/a。
6. report：config.json、result.json、report.md 原子归档，schema 固定；失败不产半份 report。

## 指标

- 诊断准确率：有效场景中根因与对象双命中比例。
- 归因正确率：仅有控制器归因证据的场景；无证据标 n/a。
- 误修复率：非必要新 ChangeRequest 的比例，文本建议不计执行。
- MTTR：t0 到 Committed 的秒数；未 Committed 不进均值。

## 入口与测试

- `python3 eval/runner.py --scenario eval/scenarios/oom-killed --mode scan`。
- `make test-eval` 使用 stdlib unittest；`eval-smoke` 运行真实单场景并明确 kind 前置。
- fake 验证成功、失败、A2A 提示不泄题、误修复、MTTR 和原子归档。
