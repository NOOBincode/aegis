# Research: kubeagent 仓库存在性 / license / 发布形式 / 诊断内核架构与数据流

- **Query**: imantaba/kubeagent 是否真实存在、可否复用其确定性诊断内核
- **Scope**: external（GitHub API + `git clone` 源码）
- **Date**: 2026-10-08

## Findings

### 仓库真实性与可达性

仓库**真实存在且公开可访问**，2026-10-08 通过 GitHub REST API 与 `git clone --depth 1` 双重验证（克隆成功，6555 KB，755 个文件）。

| 元数据 | 值 | 一手来源 |
|---|---|---|
| 地址 | `github.com/imantaba/kubeagent`（200 OK） | https://api.github.com/repos/imantaba/kubeagent |
| License | **Apache-2.0**（含 LICENSE + NOTICE 文件） | 同上 `license.spdx_id`; https://github.com/imantaba/kubeagent/blob/main/LICENSE |
| 语言 | Go（`go 1.26.4`） | `go.mod` |
| module 路径 | `github.com/imantaba/kubeagent` | `go.mod` line 1 |
| 活跃度 | 2026-06-17 创建，**最近 push 2026-10-06**（活跃维护） | API `pushed_at` |
| 规模 | 30 stars / 1 fork / 0 open issues / 未归档 | API |
| 站点 | https://k8sproject.top（homepage，mkdocs） | API `homepage` |
| 主题标签 | ai-agent, cli, client-go, devops, golang, k8s, kubectl, kubernetes, observability, sre, troubleshooting | API `topics` |

README 自我定位（原文，line 5-27）："A **read-only** Kubernetes troubleshooting CLI that tells you **why** your pods are broken"。Highlight 首条即 "🔒 Read-only by default — only get/list/watch"、"📴 **Deterministic & offline — the whole diagnostic core needs no API key**"、"📦 Single Go binary — built on client-go"、"🔌 MCP server — `kubeagent mcp` serves the same deterministic diagnosis over MCP on stdio"。这与本项目设计文档 §1.5（line 59）对 kubeagent 的描述完全一致。

### 发布形式（4 种，均为"二进制/命令"，**非可 import 的库**）

| 形式 | 命令/入口 | 证据 |
|---|---|---|
| Go 单二进制 CLI | `go install github.com/imantaba/kubeagent@latest`，子命令 `scan/watch/mcp/gate/tui/policy/rbac/fleet/...`（cobra） | README line 30; `internal/cli/*.go`; `internal/cli/cobra_behaviour_test.go` |
| 预编译二进制 | GitHub Release 附件 linux/darwin × amd64/arm64 + SHA256SUMS + cosign 签名 + SPDX SBOM + SLSA provenance | README line 488-511 |
| krew kubectl 插件 | `kubectl krew install --manifest-url=...`（未入 upstream krew-index） | README line 467-486; `krew/kubeagent.yaml.tmpl` |
| Claude Code plugin | `/plugin marketplace add imantaba/kubeagent`（内含 `kubeagent mcp` server + skills + slash commands） | README line 453-465; `.claude-plugin/plugin.json` |
| in-cluster watch 守护进程 | `kubeagent watch` + `deploy/deployment.yaml` + Helm chart `deploy/helm/kubeagent/`（暴露 `/metrics` `/issues` `/healthz` `/readyz`） | README line 382-413 |
| MCP server | `kubeagent mcp` over stdio（只读） | README line 415-423; `internal/mcp/server.go` |

**关键约束（直接决定复用路线 a 的可行性）**：所有功能代码都在 `internal/` 下（`internal/scan`、`internal/diagnose`、`internal/policy`、`internal/remediate`、……共 80+ 个包）。README 明确写道（design.md line 73）："`internal/` is a Go convention: packages under it can only be imported by code in this same module — good encapsulation for a tool that has **no public library API**"。

> Go 工具链强制：`internal/` 包只能被 `github.com/imantaba/kubeagent/...` 内部 import。aegis（不同 module path）`import "github.com/imantaba/kubeagent/internal/scan"` 会在**编译期报 "use of internal package not allowed"**。这是硬约束，不是风格问题。

### 诊断内核架构与数据流

整体是**单向流水线**（design.md line 38-43），每层一个 `internal/` 包、职责单一、公共面小：

```
flags → cluster.NewClient() → collect.* → scan.Evaluate() → report.Print()/sarif/htmlreport
        (kubeconfig→clientset)  (List/Get)   (规则判定+归因)      (text/JSON/SARIF/HTML)
```

核心编排器是 **`scan.Evaluate(ctx, client kubernetes.Interface, opts Options) (Result, error)`**（`internal/scan/scan.go` line 224-821），被 CLI `scan` 与 `watch` 守护进程共用，**只读：仅 List/Get，无写、无 LLM**（包注释 line 1-5）。数据流六阶段：

1. **连接**（`internal/cluster/client.go`）：kubeconfig → `*kubernetes.Clientset`（client-go）。
2. **采集**（`internal/collect/collect.go`）：`scan.Evaluate` 用**两阶段并发只读扇出**。phase-1 并发拉取 20 余种对象（pods/deploys/rsets/stses/dsets/jobs/cronjobs/nodes + 3 类 events + leases + svcs + endpointslices + ingresses + pvcs + namespaces + pdbs + hpas + 2 种 webhook + pvs + storageclasses + quotas + networkpolicies + 可选 tlsSecrets）。phase-2 并发拉取衍生读取（每 pod 的 `--previous` 日志、node stats、kubelet `/healthz`、apiserver `/readyz`、CoreDNS metrics）。并发上限 `defaultScanWorkers = 8`（`internal/scan/workers.go` line 18，env `KUBEAGENT_SCAN_WORKERS` 可调，1–64 钳制），经 `internal/parallel.Do` 有界 worker 池执行，错误按**索引序**返回（`errs[i]` 对应 `reads[i]`），保证确定性。
3. **判定**（`internal/diagnose/diagnose.go`）：`diagnose.Run(DefaultDetectors(now), facts)` 把每个 `Detector` 应用到每个 pod 的 `PodFacts{Pod, Events}`，命中则产出 `Finding`。详见 `kubeagent-rules.md`。
4. **组装与归因**（`internal/inventory` + 一组 `Annotate`）：`inventory.Assemble/Prioritize` 把 pod findings 聚合到 workload；随后一串**纯函数 Annotate** 依次打标——`rollouthealth`（RolloutStuck）、`rollout`（最近 rollout 关联 "what changed"）、`rootcause.Annotate/AnnotatePVC/AnnotateRegistry`（归因到宕机节点 / 坏 PVC / 共享 registry）、`netpolicy`（NetworkPolicy 提示）、`confidence.Annotate`（置信度 high/medium）。`confidence` 规则：直接读到的 K8s 状态 = high，启发式（RestartLoop/ProbeFailure/ContainerStartError）= medium；registry 归因（统计推断）= medium，node/PVC（证据支撑）= high（`internal/confidence/confidence.go`）。
5. **健康评估**（`internal/clusterhealth` 等 20 个 `*health` 包）：集群/节点/Service/Ingress/PVC/PDB/HPA/webhook/quota/证书/DNS/控制面/kubelet/磁盘/终止卡住 等各自产出 `Issue`/`Report`。部分失败不中止——RBAC 被拒的 list 记入 `Result.PartialReads`（"盲点"），永不静默当作"空"（`scan.go` line 84-90, 263-272）。
6. **输出**：`scan.Result` 结构化结果 → `internal/report/report.go`（text/JSON，2192 行渲染器）、`internal/sarif/sarif.go`、`internal/htmlreport/htmlreport.go`。严重度模型由 `internal/findings/findings.go` 统一（Info<Warning<Critical，detector 命中一律 Critical），`findings.Flatten` 把 `scan.Result` 投影成一条**确定性排序**的扁平 finding 列表，供 `gate`/`mcp`/`watch` 共用。

### Related Specs
- `.trellis/spec/k8s-read/` —— 本项目只读族包规范（kubeagent `kubeagent mcp` 是其现成件候选）
- 设计文档 §4.1 line 212（kubeagent mcp 为 L1 只读族现成件）、§4.2.2 line 235（scanner Tier-0）、§8.1 R17 line 504（复用优先/自研降级）

## Caveats / Not Found
- 深度 1 克隆不含 git 历史细节；版本号以 README/Release 为准（README 提到 v0.63.0、v1.x、兼容性从 1.0 起 MAJOR 才破稳定面）。
- 诊断内核**没有**对外 library API 是设计意图（design.md 明写），复用只能走"跑它的进程"或"借鉴设计"。
