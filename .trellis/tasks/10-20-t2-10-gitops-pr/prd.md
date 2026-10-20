# T2.10 GitOps PR 契约

## Goal

R2 通道的产物是 PR 而不是集群变更：白名单 repo、CR 绑定、五段评审模板、patch 上限、
分支名从 CR 派生；失败 = 没有 PR（fail-closed），绝不退化为直写集群。

## Requirements

1. `internal/gitops.Build(Request, whitelist)`：repo 白名单、cr_ref 合法、patch≤64KB、
   description/诊断链/影响面/风险级别/回滚方式五段齐全。
2. 分支名从 cr_ref 派生（幂等锚）；`NoClusterMutation=true` 恒真。
3. 不持有集群凭证；GitHub/GitLab API 在 adapters 层，MCP open_change_pr 后续接。
4. 单测覆盖白名单/缺段/空/超限；lint 0。

## Acceptance Criteria

- [x] PR 构造成功：白名单 + 五段 + 上限 + 分支派生。
- [x] 非白名单/缺段/空 patch/超 64KB/非法 cr_ref 全部拒绝。
- [x] 单测通过；lint 0。
- [ ] git 平台 API adapter + `mcp-servers/gitops` open_change_pr + ArgoCD sync 端到端。

## Notes

- 设计出处：ADR-001、§4.2.3、T2.10；F3/F8。
