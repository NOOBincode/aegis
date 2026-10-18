# scan — 确定性规则巡检内核（I11 Tier-0）Backend 规范

> 所属层：L2 闸门前置 · Tier-0 规则层 ｜ 里程碑：M1（落地方案 T1.11） ｜ 设计文档出处：§4.2.2 scanner、§2 I11 ｜ 关联不变量：I1/I2/I4/I11 ｜ 关联失败模式：R30（规则腐烂） ｜ Trellis 任务：`10-08-t1-4-scan-rule-layer`

## 1. 定位

`internal/scan` 是**三级诊断流水线的第一层**（Tier-0 规则 → Tier-1 归因 → Tier-2 LLM），是 LLM/人审的前置过滤器：**能规则结案的就不惊动上层**（`Finding.NextTier=false`），以此减少 AI 审核压力。全程**离线、零 LLM、零 token、零外呼**（I11/I2），断网/气隙照常工作。

**护城河提醒**：规则层只做确定性判定，不含任何放行/写判断（I1）；永不触达写能力；不进热路径、无常驻 watch（I2）。

## 2. Pre-Development Checklist

改 `internal/scan` 之前必读：

- [ ] [rules-engine.md](./rules-engine.md) —— 双层规则库契约（Detector/Policy DSL）、OOM 分级、退出码、脱敏
- [ ] [concurrency.md](./concurrency.md) —— 无状态 Engine、pmap 槽位隔离、确定性排序、热修改 fail-safe
- [ ] `.golangci.yml` depguard —— `internal/scan` 已纳入铁律 2（禁 k8s.io/sigs.k8s.io/OPA/MCP/adapters）
- [ ] `internal/adapters/k8s/README.md` —— 取数端口实现纪律（采集侧）
- [ ] 设计文档 §4.2.2、§2 I11

## 3. 目录与依赖方向

```
internal/scan/            # 限界上下文（domain 纯净 + ports + service）
├── finding.go snapshot.go      # 契约：Finding/Snapshot/PodFacts（纯结构）
├── detector.go                 # A 层：Detector 接口 + 注册表 + 9 个根因判定
├── policy.go                   # B 层：YAML 规则 DSL（schema + 校验 + Evaluate 纯函数）
├── engine.go                   # Engine.Scan 编排 A/B 层 + 确定性排序 + 结案率
├── parallel.go                 # pmap 有界并行（槽位隔离零共享写）
├── ports.go                    # SnapshotProvider / PolicySource 出站接口
├── service.go                  # 用例编排 + 热修改 fail-safe
├── report.go                   # 脱敏报告 JSON(scan-v1)/SARIF/text
├── defaultpolicies.go          # go:embed 嵌入默认规则包
└── policies/reliability.yaml   # B 层默认规则（数据，热修改）

internal/adapters/k8s/scanprovider.go   # SnapshotProvider 实现（client-go 唯一住所）
cmd/aegis-cli/scan.go + policysource.go # 装配：CLI 子命令 + YAML 解码（FilePolicySource）
```

**依赖方向**：`cmd/aegis-cli`（装配）→ `internal/adapters/k8s`（实现端口）→ `internal/scan`（domain）。反向禁止。

## 4. 关键纪律（违反即 bug）

| 纪律 | 要点 |
| --- | --- |
| 领域纯净 | domain 只用 stdlib；**YAML 解码/取数都在装配边缘/适配层**（policysource.go、scanprovider.go），不在 domain |
| 无状态 Engine | 每次调用的可变输入（policies、Snapshot）经**参数**传入，绝不存 Engine 字段（否则并发 Scan data race） |
| 确定性输出 | 结果统一排序（severity 降序 + ID 升序），与并行度无关；审计/回放/回归依赖此 |
| 盲点不静默 | 采集被拒读/部分失败记入 `Result.PartialReads`，SARIF `executionSuccessful=false`，绝不当"无异常" |
| 数据最小化 | Snapshot 不含 Secret/env/镜像仓库全文；不可信文本 `clip` 截断单行化（I4） |
| 结案标记 | `NextTier=false`=规则结案（喂 M3"规则层解决率"）；残差才置 true 上送 Tier-1/2 |

## 5. 演进与腐烂防线（R30）

- A 层新模式 = 一个 struct + `DefaultDetectors` 注册一行（不改引擎）。
- B 层新规则 = 一个 YAML 条目（热修改即生效，无需发版）。
- 规则改动会漂 golden fixture（`golden_test.go`）→ 显式更新快照并说明，防静默回归。
- 规则层解决率入 M3 eval 一等指标，持续走低 = 规则腐烂信号。
