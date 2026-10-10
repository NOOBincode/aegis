# Journal - lv (Part 1)

> AI development session journal
> Started: 2026-10-04

---



## Session 1: T1.11 确定性规则层（scanner）—— 减少 AI 审核压力
<!-- trellis-session: v=2 fp=491888a800ce887e -->

**Date**: 2026-10-10
**Task**: T1.11 确定性规则层（scanner）—— 减少 AI 审核压力
**Package**: aegis-cli
**Branch**: `main`

### Summary

设计+实现 internal/scan Tier-0 规则层（纯自研、零外部依赖、离线零 LLM）。双层规则库：A 层 9 个硬编码 Detector（含 OOM 裸退出码 137 分级推断 + 节点/容器佐证、OOM/CrashLoop 互斥、退出码解读）+ B 层 YAML 规则 DSL（热修改 fail-safe、kind 白名单排除 Secret、封闭操作集）。无状态 Engine + pmap 有界并行（槽位隔离零共享写，修复 data race）+ 确定性输出。internal/adapters/k8s 采集端口 + aegis-cli scan（JSON/SARIF/text 脱敏报告，盲点不静默）。kind 实集群验收：kubelet 报 Error+137 未标 OOMKilled 仍被 137 推断链命中。trellis-check 无 P0，1 P1(Detector 覆盖)+5 P2/P3 已修复。沉淀 spec/scan/backend 三份规范。留后续：MCP scan_cluster 形态、I4 整体包裹。

### Git Commits

| Hash | Message |
|------|---------|
| `d86d77c` | feat(scan): T1.11 确定性规则层 —— Tier-0 scanner 双层规则库 + 并发引擎 + CLI |
| `a818ecc` | docs(spec): 沉淀 scan 规则层规范 —— 双层规则库契约 + 并发/确定性约定 |

### Status

[OK] **Completed**


## Session 2: T1.12 scan_cluster MCP 工具 —— 打通 Agent→Tier-0 入口
<!-- trellis-session: v=2 fp=c05b804b6841b8ba -->

**Date**: 2026-10-10
**Task**: T1.12 scan_cluster MCP 工具 —— 打通 Agent→Tier-0 入口
**Package**: aegis-cli
**Branch**: `main`

### Summary

把 internal/scan Tier-0 规则巡检暴露为 MCP 只读工具 scan_cluster（mcp-servers/scan 独立进程，mcpserver 框架 RiskR0）。handler 经 Scanner 接口解耦，main.go 装配注入真实 service。FilePolicySource 提取为共享 internal/policyload（aegis-cli 与 scan server 共用）。四层测试：单测（参数/结构/错误协议）、白盒（元数据/schema）、黑盒（list 契约/未知工具/非法参数/panic 隔离存活）、集成（kind 注入 OOMKilled 经 MCP 命中 OOM_KILLED）+ 冒烟。trellis-check 无 P0；补 sarif 渲染 + since 负数校验 + 路径穿越防御 + gitignore 根目录二进制防护。下一步：kagent ToolServer 注册 scan server，Agent 实际调用 scan_cluster 验证端到端减负。

### Git Commits

| Hash | Message |
|------|---------|
| `91be080` | feat(scan): T1.12 scan_cluster MCP 工具 + policyload 提取 —— 打通 Agent→Tier-0 入口 |

### Status

[OK] **Completed**


## Session 3: T1.13 scan server kagent 集成（端到端打通 Agent→Tier-0）
<!-- trellis-session: v=2 fp=c5404ed370b6e168 -->

**Date**: 2026-10-10
**Task**: T1.13 scan server kagent 集成（端到端打通 Agent→Tier-0）
**Package**: aegis-cli
**Branch**: `main`

### Summary

scan server 部署进 kind 并注册 kagent MCPServer CR（SA 只读 RBAC + serviceAccountName）。端到端验证：注入 OOMKilled 经 kagent 部署的 server 调 scan_cluster 命中 OOM_KILLED（137 推断链），结果经 I4 包裹（wrapped:true），2/2 规则结案（next_tier=false，不经 LLM 即定根因）。RBAC can-i 验证最小权限。期间发现并解除环境阻塞：集群 CoreDNS/kube-proxy 系统性崩溃致 kagent controller 连不上 postgresql（该故障由 aegis-cli scan 自举诊断发现），pod 级自愈恢复。LLM 端到端 blocked（OpenAI 不可达），留私域 vLLM。

### Git Commits

| Hash | Message |
|------|---------|
| `735117f` | feat(scan): T1.13 scan server kagent 集成 —— 部署+RBAC+发现+工具调用端到端 |

### Status

[OK] **Completed**
