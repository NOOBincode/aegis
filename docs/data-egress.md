# 外部 AI 数据出域与日志处理（M1/R24）

> 适用：`aegis-diagnoser` 通过 OpenAI 兼容端点（公网 DeepSeek/其他 API、win-proxy、
> 私域 vLLM/Ollama）调用 LLM。关联不变量：I4（读路径消毒）、I6（可回放）、I10（失效自明）、
> R24（I4 只管注入不管脱敏的红队整改）。

## 1. 先承认事实

**只要使用外部 AI，就存在数据出域，无法用工程手段“消灭”。** 本文件的目标不是
“保证零出域”，而是把出域面压到可解释、可审计、可关闭：

- 哪些数据会发出；
- 发出前做了哪些最小化/脱敏；
- 哪些日志绝不落原文；
- 什么情况下应切私域/断网（Tier-0 仍可用）；
- 出域后哪些残差不可控（提供商日志/留存/训练）。

## 2. 数据路径与出域面

```
集群 API / MCP 工具
   │  raw cluster data（日志/事件/对象摘要；不可信）
   ▼
internal/sanitize
   │  1) Redact：sk-*/Bearer/JWT/私钥/连接串/key=value → [REDACTED_*]
   │  2) Neutralize：ASCII 尖括号 → 全角，防 I4 分隔符伪造
   │  3) <untrusted_cluster_data> 包裹
   ▼
LLM prompt/context（外部 AI 端点）
   │  system prompt + 用户问题 + 已脱敏/包裹的工具结果
   ▼
外部提供商（DeepSeek/win-proxy 等）——出域边界，提供商日志/留存不可控
```

**会发出：** 用户问题、system message、已脱敏的工具结果摘要/日志片段、模型生成的
候选结论。
**不会主动发出：** Kubernetes Secret 明文、kubeconfig、进程环境变量、完整
Secret/ConfigMap 数据（RBAC/工具面不读 Secret）；但日志/事件中如果本来就有
凭据，脱敏是模式化的最后一道，不是绝对保证。
**仍不可控：** 外部提供商的请求日志、数据留存、训练使用、跨境传输。这必须进入
威胁模型与部署选型评审，不能靠 prompt 解决。

## 3. 日志纪律

- MCP 审计日志只落元数据：`tool_name/risk_hint/latency/timeout/error_class/session_id/
  args_preview(redacted,≤512B)/result_sha256/result_bytes/redacted_count`；
  **不落**原始 args/result 全文。
- `internal/sanitize` 命中秘密时只计数，不记录原值；`Meta.Redacted` 进入结果元数据。
- `aegis-cli scan` 的文本/JSON/SARIF 报告在写盘前对 finding 证据/建议/盲点做同一
  `internal/redact` 脱敏，避免“CLI 报告”成为绕过 MCP 中间件的泄漏路径。
- kagent/LLM 提供商自身日志不在本仓库控制范围；生产应选支持关闭请求日志/零留存
  的私域端点，或在网关侧做请求体脱敏与配额。
- **禁止**把 API key 写进：
  - git tracked/untracked 文件、task PRD、journal、commit message、PR description；
  - shell 命令行参数（`ps` 可见）、CI 日志；
  - Agent/ModelConfig/Deployment 的 env 明文（只允许 `secretKeyRef`/Secret）。

## 4. Key 生命周期

```bash
# 首次配置/轮换：key 只经环境变量或 read -s 输入
AEGIS_LLM_API_KEY=... bash scripts/ai-up.sh --with-agent
# 或交互式
bash scripts/ai-up.sh --with-agent

# 删除 key（Agent 随后不可用；模型配置仍在）
kubectl -n kagent delete secret aegis-llm --ignore-not-found
```

- Secret `kagent/aegis-llm` 是唯一存放点；`ModelConfig` 只引用 `apiKeySecret`。
- 轮换后如控制器缓存旧值，使用 `--restart` 重启 kagent-controller/Agent。
- 仓库提交前跑 `make check-secrets`（当前树 + 可选 `--history`）；CI 跑同一脚本。

## 5. 数据分级建议（M1 最小口径）

| 级别 | 示例 | 外部 AI 允许？ |
|---|---|---|
| L0 公开 | 空集群、无业务名、合成 OOM 场景 | 可以 |
| L1 内部元数据 | namespace/deployment 名、事件 reason、节点名 | 可以，但需脱敏/最小化 |
| L2 可能含凭据 | Pod 日志、事件 message、镜像/环境变量片段 | 必须走 redact + 包裹；敏感环境建议私域 |
| L3 凭据/密钥/Secret | API key、kubeconfig、Secret data、TLS 私钥 | **禁止**进入外部 AI；工具面也不应读取 |

## 6. 残余风险与关闭开关

- 正则脱敏不完备：新格式 token、非标准凭据可能漏过；这是 **R24 的已知残差**，
  不能宣传成“零泄漏”。
- Prompt injection 仍可能影响模型判断，但工具面无写权限，影响面被 I1/I3 封顶。
- 外部 AI 不可用/断网：Tier-0 `scan` 与闸门（M2 起）必须仍可工作；这是气隙叙事。
- 真敏感环境：把 `AEGIS_LLM_BASE_URL` 指向私域 vLLM/Ollama，并在 NetworkPolicy/egress
  层拒绝公网 AI 端点；这才是“数据不出域”的工程闭环。
