# Research: kubeagent 写安全机制与脱敏报告的实现形态

- **Query**: SSAR 写前预检、漂移复检、--fix 白名单修复、审计日志、脱敏报告（HTML/JSON/SARIF）各以什么形态实现
- **Scope**: external（源码 `internal/remediate`、`internal/audit`、`internal/redact`、`internal/sarif`、`internal/htmlreport`、`internal/cli/fix.go`）
- **Date**: 2026-10-08

> 这些正是本项目设计文档 §4.2.4 line 243-244 已"吸收自 kubeagent"的机制（SSAR 预检、漂移复检）。这里给出它们的一手实现细节，供内部 `internal/scan` 与 aegis 闸门对齐。

## Findings

### 1. `--fix` 白名单修复（`internal/remediate/remediate.go`）

- **形态**：`Plan()` **纯函数**（只读、不变更、绝不 LLM 决定）产出 `[]Action`；`Apply()` 对每个 Action 做**单次受护写**。
- **白名单**：固定的 `Action.Kind`，仅 4 种——`RolloutUndo`（Deployment 回滚到前一可用 revision）、`Uncordon`（解除误 cordon 节点）；`RolloutForward`/`Cordon` 是这两者的**逆操作**（rollback 用）。其余 `Apply` 直接报 `unknown action kind`。
- **保护命名空间**：`kube-system/kube-public/kube-node-lease` 永不作为目标。
- **前置条件**（Plan 与 Apply 双重校验）：RolloutUndo 仅当 Deployment 有 ImagePull finding、`Ready<Desired`、且存在模板不同的更低 revision；Uncordon 仅当节点 cordoned 且无 `NoExecute` taint（避免打断 drain）。
- **预览 diff**：`templateChanges` 只展示 revision、每容器 image 变化与"其它 N 个字段变化"的**计数**，**绝不打印 env 值或模板内容**。
- CLI 侧（`internal/cli/fix.go`）：每个修复 `[y/N]` 交互确认（默认 No），`--dry-run` 只看预览+权限，`--yes` 免确认；每个结果写审计日志（disposition ∈ applied/refused/declined/dry-run/preflight/error/rollback）。

### 2. SSAR 写前预检（`remediate.Preflight`，remediate.go line 263-291）

- 每次写之前，向 apiserver 发 `SelfSubjectAccessReview`（`AuthorizationV1().SelfSubjectAccessReviews().Create`），核实当前身份对目标 `verb=update, group, resource, namespace, name` 是否有权。
- **fail-closed**：SSAR 调用本身出错（err≠nil）也**不写**；`allowed==false` 返回人话原因（"you lack permission to update deployments in namespace X (RBAC)"）并记 `PreflightDenied`。把权限失败暴露在审批前而非执行中——与本项目 §4.2.4 line 243 完全对应。

### 3. 漂移复检（drift re-check，remediate.go applyRolloutUndo/applyRolloutForward）

- `Apply` 时**重新 Get** Deployment + List ReplicaSet，重算 `pickTarget`；若当前 revision / 目标 revision 与 preview 时记录不符 → `Refused`("state changed since preview … no write made")。
- **rollback 用基于内容的漂移键**（Amendment 2026-07-25）：因 Kubernetes 在模板恢复后会分配全新 revision 号，数字键永不匹配，故改为校验"Deployment 当前每容器 image 仍等于 fix 时记录的目标 image"，任何不符即拒绝（不蛮写）。
- 与 aegis §5.1 `inverseVerified`、§6 F6（逆操作失效→回滚前 dry-run 复验漂移）同构。

### 4. 审计日志（`internal/audit/audit.go`）

- **追加式 JSON-Lines**，一行一个 `Record{Time(RFC3339 UTC), Kind, Namespace, Name, Target, Changes, Disposition, Detail, FromRevision, ToRevision}`。
- **只记安全展示值**（revision/image/count + 自有 detail 文案），**绝不记 pod spec/env/secret**（包注释 line 1-4）。
- `ReadLast(path, want)` 倒序扫描取最近一条 `Disposition=="applied"`，跳过畸形/截断行——`--rollback` 据此构造 `remediate.Inverse` 逆操作，走同一套 guard rails。

### 5. 脱敏（`internal/redact/redact.go` + `internal/safetext`）

- `redact.URL`：URL 降为 `scheme://host`，去掉 path/query/fragment/userinfo；解析不出则 `(redacted)`。
- `redact.Addresses`：正则把自由文本里的 `host:port` / IPv4(:port) / `[v6]:port` 改写为 `<redacted>`（用于 `--logs` 抓到的容器日志里的连接目标）；刻意**不**匹配无端口单词以免误伤版本号（R248）。
- `redact.Error`：`net/http` 的 `*url.Error` 会内嵌完整请求 URL，`Error` 沿链逐层脱敏 URL 而保留 operation 与底层原因。MCP `guard`（`internal/mcp/server.go`）把 panic 转成错误结果时也走它，防止把 URL 漏进工具结果。
- `safetext.Line`（多处调用）：在 ingress 处对集群返回的自由文本（event message、termination reason、policy message）做单行化/控制字符清洗，再进终端/JSON/HTML。policy 的 evidence 另加 120 字符截断（`policy/eval.go evidenceLimit`）。

### 6. 脱敏报告输出（HTML / JSON / SARIF）

- **HTML**（`internal/htmlreport/htmlreport.go` + `report.html.tmpl`）：**单文件、零 JS、零外部 CSS/字体/图片**，可离线打开、可在严格 CSP 下渲染、可直接挂到工单；**不含任何集群身份**——无 context 名、无 apiserver URL、无 kubeconfig 路径。用 `html/template`（非 text/template）做上下文自动转义，因为容器 termination message / event reason 是集群控制的自由文本、会被转发。Blind spots（`scan.Result.PartialReads`）也渲染进报告，避免"看着绿其实瞎"。
- **JSON**：`scan.Result` → `findings.Flatten` 的确定性排序扁平列表；六种版本化 JSON 文档（scan-v1、gate-v1、fleet-v1、rbac-check-v1、rbac-print-v1、watch-issues-v1、watch-explanations-v1、baseline-v1）有 schema（`website/docs/schemas/*.json`）。
- **SARIF 2.1.0**（`internal/sarif/sarif.go`）：把 gate verdict 渲染成可上传 GitHub code scanning 的 SARIF。finding 是集群对象无文件可指，故合成 `k8s://<ns>/<Kind>/<name>` URI、不带 region。severity 映射 Critical→error / Warning→warning / Info→note。**只声明实际触发的 rule**；`executionSuccessful` 在有未豁免盲点或 timeout 时置 false，避免"gate 根本没看到集群却上传出干净结果"。policy 规则的 Issue 以 `policy/<ruleID>` 为 SARIF ruleId，与同名 detector 区分。

### 7. CI gate 退出码契约（`internal/gate/gate.go`）

退出码是**发布的稳定契约**：`0` pass / `1` fail / `2` inconclusive / `3` timeout / `4` usage；配 `--fail-on`(critical/warning/info) 阈值与 `--wait-for`（只判定归因于某次 rollout 的 finding）。无任何 gate 路径调 LLM。

### 8. MCP server（只读薄封装面，`internal/mcp/`）

- 工具（stdio, `modelcontextprotocol/go-sdk`）：`kubeagent_triage`（整簇/单 ns 确定性诊断→verdict+findings+coverage）、`kubeagent_inspect`（单对象描述）、`kubeagent_advisory`（按 section 取 security/certificates/fix 等）、`list_contexts`（仅 `--allow-context-switch` 时注册）。**永不写集群**。
- `kubeagent_triage` 内部就是调 `scan.Evaluate(...)`（triage.go line 68），即**复用同一诊断内核**——这是路线 b 的关键证据。
- 每个 handler 用 `guard` 包 recover，防单个 panic 拖垮整个 stdio server；错误文案经 `redact`、不含 kubeconfig 路径/apiserver 地址。

### Related Specs
- `.trellis/spec/gatekeeper/`、`/controller/`、`/k8s-write/` —— aegis 闸门/写路径规范（与上述机制对齐的对象）
- 设计文档 §4.2.4 line 243-244、§5.1、§6 F6、§8.1 R16 line 503

## Caveats / Not Found
- 未逐行读 `internal/gate/gate.go`（退出码语义取自包注释 line 20 与 README line 431-437）、`internal/htmlreport/report.html.tmpl` 模板正文、`internal/safetext` 全部实现。`--fix` 当前仅 2 类正向修复（RolloutUndo/Uncordon），覆盖面小于 README 暗示的"白名单修复"通用能力。
