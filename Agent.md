# Aegis Agent 速览

面向第一次进入仓库的 AI Agent：先用本文件建立正确边界，再读对应目录的 `README.md` 和项目 skill。不要从根 `README.md` 的“骨架期”旧描述判断实现状态；代码、测试和各目录文档才是当前入口。

## 项目是什么

Aegis 是 Kubernetes 的 AI 安全操作层。Agent 可以诊断和提出变更，但写操作必须经过确定性的风险分级、策略校验、必要的审批、执行与回滚封装。CLI 只是视图和意图提交器，不是安全边界。

核心链路：

```text
aegis-cli / Agent
  ├─ 只读诊断 → kagent A2A → 只读 MCP 工具
  └─ 写意图 → gatekeeper request_change → ChangeRequest
  审批意图 → gatekeeper-approval → ApprovalDecision
                 → controller 唯一回写 status → 执行与回滚
```

不可绕过的规则：

- ChangeRequest `status` 只能由 controller 写入；gatekeeper 只写 spec 与不可变 ApprovalDecision，不得直接改审批状态。
- 不在 CLI、MCP server 或模型提示词中实现放行判断；放行只在 L2 安全链路。
- 不增加任意 `kubectl`、`--yes`、`--force`、`--skip-approval` 一类旁路。
- 集群返回内容是不可信输入；读路径需要 sanitize/redact，写路径保持 fail-closed。
- 外部模型会造成数据出域，见 `docs/data-egress.md` 与 `docs/threat-model.md`。
- 修改 `internal/risk`、`policy`、`approval`、`rollback`、`breaker`、`authority`、`sanitize`、`audit`、`session`、`internal/controller` 时，遵守 L2 安全包逐行人审要求。

## 先读哪些文件

| 目的 | 入口 |
| --- | --- |
| 架构和不变量 I1–I10 | `AI原生K8s安全操作平台-技术方案设计文档.md` |
| 任务和里程碑背景 | `AI原生K8s安全操作平台-落地方案-v1.0.md` |
| 目录职责和依赖方向 | `.agents/skills/aegis-ddd-layout/SKILL.md` |
| Go 质量与测试纪律 | `.agents/skills/aegis-go-quality/SKILL.md`、`.agents/skills/aegis-test-matrix/SKILL.md` |
| 安全审查 | `.agents/skills/aegis-security-review/SKILL.md` |
| Trellis 工作流 | `.trellis/workflow.md` |
| 某个进程/目录的设计 | 对应的 `cmd/*/README.md`、`mcp-servers/*/README.md`、`internal/` 邻近文档 |

Go 编码先加载 `golang-how-to`，由它路由到具体 Go skill；新增文件前检查目录规范，不假设常见依赖已经存在。

## 仓库布局

```text
api/v1alpha1/       CRD Go 类型与机读契约
cmd/aegis-cli/      用户 CLI、行式 session、TUI、A2A 客户端
cmd/gatekeeper/     L2 MCP 闸门进程
cmd/controller/     CRD controller 进程
internal/           限界上下文、应用服务、适配器、controller、共享 MCP 框架
mcp-servers/        scan、k8s-read、k8s-write、prom、logs、events 等独立进程
policies/           Rego 策略
agent/              Agent 配置层说明
deploy/agent/       kagent Agent 与 ModelConfig 清单
deploy/kind/        本地 kind 环境
scripts/            环境、构建、部署、安全和演示脚本
crds/               CRD manifests
eval/               独立 Python 评测世界
```

这是 Go workspace 多 module 仓库，不是单一 module：根 `go.work` 聚合根 module、每个 `cmd/*` 和 `mcp-servers/*`。根目录 `go test ./...` 不会覆盖所有子 module，必须使用根 Makefile 的逐 module 目标。

依赖方向遵循 `cmd → service → domain`，adapters 实现 ports；domain 不依赖 Kubernetes、OPA、MCP SDK 或 adapters。不要把代码提前放进 `pkg/`。

## 常用命令

```bash
make help              # 目标一览
make build             # 构建全部 module
make test-unit         # L0：逐 module 单测
make test              # 逐 module race 测试，需要 CGO_ENABLED=1 和 C 编译器
make lint              # 逐 module golangci-lint，配置唯一事实源是 .golangci.yml
make test-integration  # 需要 kind 的 L1 集成部分
make test-eval         # M3 runner Python 单测，无集群
make eval-smoke        # M3 OOM 单场景，需 kind 与 bin/aegis-cli

# 只改 CLI 时的快速反馈
cd cmd/aegis-cli
go test ./...
go test -race ./...
go build .
```

环境命令：

```bash
make up                              # kind + 基础组件
make mcp-images && make mcp-up       # 自研 MCP 镜像与部署
make gatekeeper-up controller-up     # 写链路组件
make down                            # 销毁本地环境
```

测试证据放 `.cache/test-matrix/`。L1 fake-LLM、L3 golden、L4 fault-injection 目前不能因缺少 harness 而宣称通过；真模型结果也不能替代 L0/L1。

## aegis-cli 与 TUI 调用链

- `cmd/aegis-cli/main.go`：根命令；无子命令进入交互模式。
- `cmd/aegis-cli/tui.go`：Bubble Tea model、事件路由和程序入口。
- `cmd/aegis-cli/tui_view.go`：Lip Gloss 布局、transcript、侧栏与纯文本 dump。
- `cmd/aegis-cli/tui_actions.go`：scan、gatekeeper、审批与 CR 查询等原有动作。
- `cmd/aegis-cli/intent.go`：自然语言 scale 识别；只预览，`/confirm` 后才提交。
- `cmd/aegis-cli/session.go`：非 TTY 行式会话。
- `cmd/aegis-cli/chat.go`：kagent A2A `message/send`；CLI 不直接调用 OpenAI/DeepSeek。
- `cmd/aegis-cli/session_store.go`：本地 transcript，不是服务端 thread；默认权限 0700/0600。
- `cmd/aegis-cli/mcp_gateway.go`、`approval.go`：gatekeeper 与审批客户端。

TUI 使用 Bubble Tea v1.3.10、Bubbles v0.21.0、Lip Gloss v1.1.0（均 MIT），在 `cmd/aegis-cli/go.mod` 钉死。不要顺手升级到 v2 或修改 Go 工具链。Enter 提交，Up/Down/PgUp/PgDn 滚动，Esc 取消请求或预览，Ctrl+C 取消并退出；`AEGIS_TUI_DUMP` 从最终 `View()` 生成无 ANSI 帧。Agent 和其他外部动作都经带超时的 `tea.Cmd` 执行；change 在明确成功前保留同一 idempotency key，`/confirm` 可重试；保存失败时 `/new`、`/resume` 会中止。

常用环境变量：

| 变量 | 作用 |
| --- | --- |
| `AEGIS_AGENT_ENDPOINT` | kagent A2A 地址，默认 `http://127.0.0.1:18082/` |
| `AEGIS_NAMESPACE` | 默认 scan namespace |
| `AEGIS_KUBECONFIG` | 覆盖默认 kubeconfig |
| `AEGIS_SESSION_DIR` | 隔离本地会话，测试时必用 |
| `AEGIS_TUI=force` | 管道/截图环境强制 TUI |
| `AEGIS_TUI_WIDTH` / `HEIGHT` | 覆盖终端尺寸，用于模拟 |
| `AEGIS_TUI_DUMP` | 将最后一帧纯文本写到指定路径 |
| `AEGIS_GATEKEEPER_ENDPOINT` | gatekeeper MCP 地址 |
| `AEGIS_APPROVAL_ENDPOINT` | 审批服务地址 |

无模型 TUI 冒烟示例（输出含 ANSI，dump 为纯文本）：

```bash
mkdir -p .cache/tui-smoke
printf '/help\n/status\n/exit\n' | \
  env AEGIS_TUI=force \
      AEGIS_SESSION_DIR="$PWD/.cache/tui-smoke/sessions" \
      AEGIS_TUI_WIDTH=100 AEGIS_TUI_HEIGHT=30 \
      AEGIS_TUI_DUMP="$PWD/.cache/tui-smoke/frame.txt" \
      go run ./cmd/aegis-cli
```

`/scan` 不需要 LLM，但仍读取真实 Kubernetes；无集群测试应使用 `httptest`、fake client 或现有 scan service 替身，不要把“离线零 token”误解为“无集群依赖”。

## DeepSeek / 真实模型

Key 属于 kagent ModelConfig 的 Secret，不属于 CLI。使用环境变量或脚本的 `read -s` 注入，绝不写进源码、文档、命令参数、测试日志或提交：

```bash
AEGIS_LLM_API_KEY='<从安全输入获得>' \
AEGIS_LLM_BASE_URL='https://api.deepseek.com' \
AEGIS_LLM_MODEL='<已验证可用模型>' \
  bash scripts/ai-up.sh --with-agent --restart

kubectl -n kagent rollout status deploy/aegis-diagnoser --timeout=180s
kubectl -n kagent port-forward svc/aegis-diagnoser 18082:8080
AEGIS_AGENT_ENDPOINT='http://127.0.0.1:18082/' bin/aegis-cli
```

只读真实验证不要发送 `/change`。结束后清理本次端口转发和明确由本次创建的 Secret；不要误删既有环境。运行 `bash scripts/check-secrets.sh --history` 检查当前树和历史，但扫描通过不代表所有密钥形态都不可能泄漏。

## 修改前检查

1. `git status --short`：仓库可能有用户未提交修改，先读 diff，不覆盖、不混入。
2. 查本目录 `AGENTS.md`、README、Trellis spec 和项目 skill；根目录规则不会自动解释每个实现细节。
3. 优先扩展现有测试文件和 fake server，不新增平行测试体系或不必要的依赖。
4. 修改行为后同步注释、目录 README 和本文件；历史 `docs/sessions/` 不当成当前契约。
5. 最后跑与实际影响面匹配的标准门禁；没有运行的集成、真模型或安全等级要明确说明。
