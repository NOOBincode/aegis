# T1.6 L3 运行时接入：LLM 通道（win-proxy）+ Agent 编排 + 自主诊断 OOMKilled

## Goal

接通 LLM 通道（kagent ModelConfig openAI provider 指向 win-proxy 172.25.16.1:15721，agnes-2.0-flash，免认证，kind pod 可达）+ 配置诊断 Agent CR（systemPrompt + tools 白名单含 scan_cluster/k8s-read + modelConfigRef）。端到端验收①：注入 OOMKilled，Agent 自主诊断（调 scan_cluster/k8s-read）输出根因报告命中标注。双通道设计：公网 API + 私域/本地 LLM（win-proxy 代理 + local LLM 接缝预留）。

## Requirements

- TBD

## Acceptance Criteria

- [ ] TBD

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
