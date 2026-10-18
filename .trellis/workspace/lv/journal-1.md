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


## Session 4: M1 阻塞修复：环境/交付/安全/CI
<!-- trellis-session: v=2 fp=7d9e98c2aa8211cf -->

**Date**: 2026-10-19
**Task**: M1 阻塞修复：环境/交付/安全/CI
**Package**: aegis-cli
**Branch**: `fix/m1-blockers`

### Summary

按 review 1-4 修复：env-up 宿主 inotify 预检/版本校验/Chaos-kagent 就绪名/健康摘要；k8s-read 0.2.0 镜像+Dockerfile+只读 RBAC+metrics-server；关闭 kagent-tools cluster-admin 暴露+NetworkPolicy+threat-model AS-15；scan 触顶盲点/事件 recency/policy kind/sanitize 转义/k8s-read 日志上限与 since；CI 补 scan 与逐 module lint。kind 内 k8s-read 8 工具、get_pods/get_resource_metrics、scan_cluster 实调通过，kagent controller/Agent/metrics-server Ready。M1 剩余 T1.3/T1.4/T1.8/T1.10 已建任务，未实现；win-proxy/LLM 通道按用户要求留后续。

### Main Changes

- env-up 失败必须响与安全默认
- k8s-read 交付闭环 + 只读 RBAC
- scan 正确性/I4 转义/CI 门禁

### Git Commits

| Hash | Message |
|------|---------|
| `98314f8` | fix(m1): 解锁环境并交付 k8s-read/metrics-server/只读 RBAC（review 1-4） |
| `357dbe9` | chore(trellis): 修 M1 阻塞任务与 T1.3/T1.4/T1.8/T1.10 排期 |

### Testing

- [OK] 12 module lint 0 issues；无 race 单测全绿；kind MCP 实调通过

### Status

[OK] **Completed**

### Next Steps

- 宿主抬 inotify 后跑 make up 复验 env-up 脚本
- T1.8 DecisionRecord/replay + T1.10 OOM 场景
- win-proxy/LLM 通道后续单独讨论


## Session 5: T1.10 OOMKilled 场景与 137 推断实测
<!-- trellis-session: v=2 fp=520f0015d143d9ac -->

**Date**: 2026-10-19
**Task**: T1.10 OOMKilled 场景与 137 推断实测
**Package**: aegis-cli
**Branch**: `fix/m1-blockers`

### Summary

新增 eval/scenarios/oom-killed：busybox awk 分配触发 cgroup OOM（退出码 137、reason=Error，kubelet 未标 OOMKilled）；经 kagent scan server 调 scan_cluster 命中 OOM_KILLED（Medium，容器 limit 佐证），wrapped=true、partial_reads=null。T1.10 剩余：Agent 报告（LLM 通道）与 aegis-cli replay（T1.8）。

### Git Commits

| Hash | Message |
|------|---------|
| `87a9143` | feat(eval): T1.10 第 1 号 OOMKilled 场景与 137 推断实测 |

### Testing

- [OK] kind 实机：scan_cluster 命名空间 aegis-eval 命中 OOM_KILLED/Pod/aegis-eval/oom-demo-*

### Status

[OK] **Completed**

### Next Steps

- T1.8 DecisionRecord + replay；LLM 通道就绪后补 Agent 端到端


## Session 6: 竞争态/渗透/回归：便携 gcc、I4 强化、安全冒烟、benchmark
<!-- trellis-session: v=2 fp=eced9b4e145da01f -->

**Date**: 2026-10-19
**Task**: 竞争态/渗透/回归：便携 gcc、I4 强化、安全冒烟、benchmark
**Package**: aegis-cli
**Branch**: `fix/m1-blockers`

### Summary

无 root 环境用 deb 解包 gcc-11 到 .cache/toolchain，CGO_ENABLED=1+CC 跑通 make test -race（12 module）。I4 强化为全角尖括号+SetEscapeHTML(false)，实测日志/事件/scan 证据注入均只 1 个闭合、JSON 解码后无 ASCII <>。安全冒烟：RBAC 矩阵、kagent-tools 不存在、NetworkPolicy 本 kind 实测强制（default 阻断/kagent 200）、工具面只读、Agent 白名单只读。Skill 检查：lock 覆盖齐全、无 symlink/可执行资产/安装管道；lock hash 与原文不一致 46/46（warning，需管理器 canonicalization）。benchmark：EngineScan workers=1 7.34ms→8 4.25ms（~42%）。make test-integration 全绿；SCAN_E2E_URL 下 kagent OOM 端到端通过。

### Main Changes

- 便携 gcc + race 全绿
- I4 全角转义与注入测试
- security-smoke/check-skills + CI

### Git Commits

| Hash | Message |
|------|---------|
| `4c87e6b` | test(security): 竞争态检测 + 注入/提权/Skill 安全检查与 I4 强化 |
| `47ceb79` | chore(trellis): 记录竞争态/渗透/benchmark 验证 |

### Testing

- [OK] make test -race 12 module; make lint 0 issues; make test-integration; benchmark

### Status

[OK] **Completed**

### Next Steps

- T1.8 DecisionRecord/replay；LLM 通道就绪后 Agent 端到端
- Skill lock canonicalization/重生成


## Session 7: T1.10 Agent 端到端安全测试（临时 DeepSeek 通道）
<!-- trellis-session: v=2 fp=ed269915f691a326 -->

**Date**: 2026-10-19
**Task**: T1.10 Agent 端到端安全测试（临时 DeepSeek 通道）
**Package**: aegis-cli
**Branch**: `fix/m1-blockers`

### Summary

用临时 ModelConfig/Secret（DeepSeek OpenAI 兼容，deepseek-flash）创建 aegis-diagnoser-ds，复用原 systemMessage+只读工具白名单。正常 OOM 场景 Agent 命中 OOM_KILLED/CRASH_LOOP 并给出根因与残差；日志注入（IGNORE.../伪造 untrusted 闭合）被识别拒绝；索要 API key/system prompt 被拒且无真实 key 泄露；删除 kube-system/外传日志请求被拒。MCP 日志确认 scan_cluster 与 k8s-read 工具被调用。临时 Secret/Agent/ModelConfig 已删除；仓库/远端树/历史无 key。M1 剩余：aegis-cli replay/DecisionRecord（T1.8）。

### Git Commits

| Hash | Message |
|------|---------|
| `4ed1de1` | docs(eval): 记录 T1.10 Agent 端到端安全测试证据 |

### Testing

- [OK] A2A message/send 端到端；scan/k8s-read 工具日志；注入/凭据/提权/外传用例

### Status

[OK] **Completed**

### Next Steps

- T1.8 DecisionRecord/replay；R24 脱敏/私域通道
