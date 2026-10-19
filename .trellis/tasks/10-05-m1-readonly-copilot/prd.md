# M1 只读诊断 copilot — 前三任务 + 记忆存储选型

## Goal

M1 前三任务（T1.1 工具框架与注册 / T1.2 只读工具集 / T1.3 观测数据工具）+ 后续记忆存储与数据库选型。每工具需冒烟+单测+集成测试。验收：M1 口径=OOMKilled 诊断 + aegis-cli replay 回放。

## Requirements

- TBD

## Acceptance Criteria

- [x] M1 验收口径：OOMKilled 注入 → scan_cluster 命中 + Agent 根因报告 + `aegis-cli replay` 回放（T1.10，2026-10-19 full demo）。
- [x] 只读工具面：scan 1 + k8s-read 8 + prom 2 + logs 1 + events 1（T1.3）；全走 mcpserver 中间件。
- [x] 双预算/审计/脱敏：T1.4、T1.8；外部 AI key 安全流程（ai-up/ai-down）。
- [x] T1.6 Agent 运行时：kagent ModelConfig/Agent 配置 + 自主诊断；原 win-proxy 旧端点未复活，DeepSeek OpenAI 兼容通道实机验证。
- [ ] T1.7 记忆存储选型：属于 M1 后（post-M1）规划，不阻塞 M1 验收。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
