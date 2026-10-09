# T1.11 确定性规则层（scanner / Tier-0）— 执行计划

> 状态：**定稿**——复用路线定案为**纯自研**（`design.md §4`：不 import kubeagent 代码、不依赖其 mcp 件，仅借鉴设计）。
> 每步完成后跑对应验证命令；任何一步不过即回滚到上一稳定点（见 §回滚）。

## 前置

- [x] 0. kubeagent 调研：结论落盘 `research/`（overview/rules/mechanisms/reuse-decision 四份），复用路线已定稿。**（2026-10-08 完成）**

## 步骤（有序）

- [ ] 1. **骨架与契约**：建 `internal/scan` 包：`doc.go`（包定位/纪律）、`finding.go`（Finding/Severity/Confidence/ObjRef）、`snapshot.go`（Snapshot/PodFacts/*Info 纯结构）、`detector.go`（Detector 接口 + Run 扇出）、`engine.go`（Engine.Scan 编排 A/B 层 + 聚合 + 确定性排序 + PartialReads 盲点）。
  - 验证：`go build ./internal/scan/... && go vet ./internal/scan/...`
- [ ] 2. **适配层**：`internal/scan/k8sadapter/`，经 `internal/adapters/k8s` 出站端口取数；两阶段有界并发只读采集（≤8 worker，错误按索引序返回保确定性）→ `Snapshot`（pods/deployments/nodes/events/metrics，数据最小化裁剪敏感面）。
  - 验证：`go test ./internal/scan/k8sadapter/...`（fake clientset 注入，断言 Snapshot 聚合正确 + 拒读记入盲点）
- [ ] 3. **A 层 Detector 规则库**（硬编码 Go）：实现 OOM_KILLED/CRASH_LOOP/PENDING_UNSCHEDULABLE/PROBE_FAILING/IMAGE_PULL_ERROR/RESTART_LOOP/CONTAINER_START_ERROR/CONFIG_ERROR，一模式一文件 + `DefaultDetectors(now)` 注入时钟。
  - 验证：`go test ./internal/scan/...`（每 detector 命中/不命中/边界 三类表驱动用例，注入时钟保确定性）
- [ ] 4. **B 层 YAML 规则 DSL**（数据驱动，抗 R17，**热修改**）：`policy.go`（{id,match,assert,level,message} schema + 封闭 10 操作 + 加载 fail-fast 校验 + kind 白名单排除 Secret/禁读 ConfigMap data）+ `policies/reliability.yaml` 首批规则 + `Evaluate` 纯函数 + **热加载**（Scan 前重载/失效缓存，坏规则 fail-safe 回退已知良好集并告警）。
  - 验证：DSL 校验表驱动用例（重复 id/非法 op/涉密 kind 全被拒）；Evaluate 纯函数用例；**热修改用例**（改 YAML 后新一轮 Scan 生效、坏规则回退良好集）
- [ ] 5. **报告渲染**：`report.go` 输出版本化 JSON（scan-v1）+ SARIF 2.1.0（`k8s://` URI、只声明触发 rule、盲点置 executionSuccessful=false）；脱敏剥离集群身份/内网 IP/secret/env/镜像路径。
  - 验证：单测断言报告不含预设敏感字段 + 盲点渲染
- [ ] 6. **CLI 接入**：`cmd/aegis-cli scan` 子命令（构造 adapter → Engine → 打印报告）。
  - 验证：`go run ./cmd/aegis-cli scan --kubeconfig <kind>` 输出 findings
- [ ] 7. **MCP 形态（可选/并列）**：注册只读工具 `scan_cluster`（`roMeta`，RiskR0），经 `internal/mcpserver` 框架，结果走中间件包裹/限流。
  - 验证：复用 T1.1 黑盒/白盒模式（未知工具/超限/schema 契约/超时）
- [ ] 8. **I4 包裹**：Finding/报告进入 LLM 上下文处经 `internal/sanitize` 包裹 `<untrusted_cluster_data>`（M1 形态 T1.5 库函数）。
  - 验证：集成测试断言包裹标记存在
- [ ] 9. **离线验收**：断网/无 LLM key 下 kind 注入 OOMKilled，`aegis-cli scan` 命中根因标注（M1 验收口径）。
  - 验证：手工/脚本演示，对照期望根因
- [ ] 10. **正确性 golden/fixtures**：为每个 A 层 Detector 与 B 层规则族建 golden 场景 fixture（构造 Snapshot/输入 → 期望 Finding 快照），纳入回归；M1 OOMKilled 场景固化为 eval 场景库第 1 号（衔接落地方案 T1.10）。
  - 验证：golden 测试全绿；改规则导致快照漂移即测试失败（腐烂/回归信号）
- [ ] 11. **落地方案补记**：§4 M1 表补 T1.11 行 + 编号冲突说明（T1.1 工具框架 / T1.4 双预算已占用）。
- [ ] 12. **全量检查**：`make build && make lint && go test ./...` 全绿；depguard 不违规（引擎不 import client-go/k8s.io）。

## 评审门

- **规划评审（本步）**：prd/design/implement 三件套齐备 + 复用路线定稿 → 经用户评审后 `task.py start`。
- **实现中期**：步骤 3–4 完成后自评规则库覆盖度与契约稳定性。
- **合入前**：步骤 12 全绿 + `trellis-check` 过不变量检查单（I1/I2/I4/I11）。

## 回滚点

- 步骤 1–2 为纯新增包，回滚 = 删 `internal/scan`，无既有代码改动风险。
- 步骤 6–7 接入 CLI/MCP，回滚 = 摘除注册/子命令，引擎与适配保留。
- 步骤 10 golden 快照漂移 → 定位是规则改动还是回归；规则改动需显式更新快照并说明，回归则修复规则。

## 执行进度（2026-10-09）

**已完成**：步骤 0–6、9–12。
- 0 调研定案（纯自研）、1 骨架契约、2 适配层（`internal/adapters/k8s/scanprovider.go`，fake 端到端验证）、3 A 层 Detector（OOM 分级推断/CrashLoop/Pending/ImagePull/Evicted + 退出码解读）、4 B 层 YAML DSL（热修改 fail-safe）、5 报告（JSON/SARIF/text）、6 CLI（`aegis-cli scan`）、9 离线验收（kind 实集群注入 OOMKilled，kubelet 报 Error+137 未标 OOMKilled，被 137 推断链命中 medium）、10 golden（OOM 场景固化 + 确定性）、11 落地方案补记 T1.11、12 全量检查（build/lint 0 issues/test 全绿，scan 75.5%、adapters 78%）。
- 额外加固：并发改造——Engine 无状态化（修复 `engine.policies` 共享写入的 data race）、A/B 层有界并行扇出（槽位隔离零共享写）、事件预索引 O(events)、确定性输出与并行度无关；并发安全/确定性测试全过（`-race` 留 CI，本机无 gcc）。

**未做（留后续任务）**：
- 步骤 7 MCP 形态（`scan_cluster` 工具）：本轮以 CLI 为消费形态验收，MCP 封装与 T1.2 只读族收敛时一并做（避免与 T1.2 在制品冲突）。
- 步骤 8 I4 整体包裹（`<untrusted_cluster_data>`）：Finding 字段级消毒（clip）已做；整体包裹属进 LLM 上下文场景，随 MCP 形态（T1.5 中间件）一并落地。

## 独立检查（trellis-check，2026-10-10）与修复

无 P0 blocker。发现 1 条 P1 + 5 条 P2/P3，全部修复并复测（lint 0 / 测试全绿 / 真实集群 8 类规则命中）：
- **P1 A 层覆盖不足**：补齐 PROBE_FAILING/RESTART_LOOP/CONTAINER_START_ERROR/CONFIG_ERROR 四类 detector（5→9 个，覆盖设计 8 类 + EVICTED）；PROBE_FAILING 消费 PodFacts.Events 事件佐证（盘活预留字段）。
- **P2 SARIF 规则顺序不确定**：rules 数组按 id 排序，输出字节级确定。
- **P2 默认规则包无测试兜底**：新增 cmd/aegis-cli 测试断言 DefaultReliabilityYAML 可解码+校验通过+含预期 id（抗 R17）。
- **P2 EvaluatePolicies 空 Values panic**：assertViolated 对需右值 op 加空值防御（视为不违反）。
- **P2 事件时间窗未启用**：实现 SinceSeconds 过滤（注入时钟），CLI 加 `--since`（默认 24h），SystemOOM/探针佐证不再误用陈旧事件。
- **P3 OOM/CrashLoop 互斥**：OOMKilled 容器不再重复报 CRASH_LOOP/RESTART_LOOP（去噪，减少审核压力）。
- **P3 quantity 陷阱**：mustQuantity 支持小数（1.5Gi）；数值 op 的 values 加载期 isValidQuantity 校验。
