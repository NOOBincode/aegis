# docs/sessions — 真实交互留痕

本目录保存**真实人机交互**（CLI + kagent + LLM）的可审查记录，用于：

- 证明交互路径真实可用，而非只用单测；
- 复盘 Agent 发现的运行态问题；
- 对照代码修复前后行为。

纪律：

1. 日志里**不得出现 API key、Bearer token、Secret 值**；提交前跑 `scripts/check-secrets.sh --history`。
2. 真实 key 只存在 `kagent/aegis-llm` Secret；会话结束/轮换后应删除（`scripts/ai-down.sh`）。
3. 原始日志保留一次真实输出；摘要文档说明输入命令与结论，避免把输入伪装成回显。
4. 发现的问题必须回写对应任务/代码/测试，不允许只留在日志里。

- `2026-10-21-tui.md` + `tui-2026-10-21.png`：TUI 设计迭代与最终截图。
