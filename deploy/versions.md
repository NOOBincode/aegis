# versions.md — 组件版本钉死表

> 地位：全仓库版本单一事实源（落地方案 §9；aegis-ci 规范）｜ 生效里程碑：W1 初稿入库，W1 核验后逐行钉死 ｜ 设计文档出处：§3.2、§3.3 ｜ 关联不变量：I8（eval 可复现以环境可复现为前提）｜ 关联失败模式：F14（环境漂移导致失效语义不可复现）

## 版本表

> 机器可读形式：`deploy/versions.env`（env-up.sh / CI 唯一 source 版本来源），与本表必须同步。

| 组件 | 类别 | 版本 | 来源与约束 |
| --- | --- | --- | --- |
| kind | 本地/CI 集群 | v0.24.0 | 落地方案 §9；与 kubectl 版本兼容矩阵一并核验 ✅已核验（本机实装） |
| kubectl | 客户端 | v1.37.1 | 与 kind 内 apiserver 版本兼容 ✅已核验（本机实装） |
| helm | 包管理 | v3.22.0 | 安装 Chaos Mesh/ArgoCD/kagent 使用 ✅已核验（本机实装） |
| Go | 工具链 | 1.26（go.work `go 1.26`，本机 1.26.8） | toolchain 以 go.work 为准（落地方案 §9）；CI 用 setup-go 的 go-version-file 读取 |
| Python | eval 运行时 | ≥3.11 | 落地方案 §9 底线；仅 `eval/` 使用，控制面禁止引入 Go 以外运行时（aegis-go-quality） |
| golangci-lint | Lint | 2.14.0 | `.golangci.yml` 是唯一事实源，本地 make lint 与 CI 同配置 ✅已核验（make lint 0 issues） |
| kagent | Agent 运行时（L3） | TBD（T0.4 钉死） | 钉 release tag，禁止跟踪 main（落地方案 §9）；fork 边界见 `docs/adr/ADR-002.md` |
| agent-sandbox | 沙箱底座（L0-b） | 限定 v1.0.x 系列（T1.9 集成） | 设计文档 §3.3/N1：SIG Apps 现成件（v1.0.2 已发布），隔离委托 gVisor/Kata |
| Chaos Mesh | 故障注入 | chart 2.7.2（app 2.7.2） | M1 验收演示与 M3 场景库（T3.2）注入工具 ✅chart 版本已核验（helm repo 可达） |
| ArgoCD | GitOps（R2 通道） | chart 7.6.12（argo-cd） | ADR-001：R2 强制 PR 通道；T2.10 ⚠️chart 版本待核验（argo-helm 仓库当前网络不可达） |
| Prometheus | 指标 | kube-prometheus-stack chart 65.5.0（含 Grafana） | 闸门核心指标唯一入口（`deploy/observability/README.md` §4.3） ⚠️chart 版本待核验（仓库当前网络不可达） |
| OTel collector | 遥测管道 | otel/opentelemetry-collector-contrib:0.120.0 | traces→Langfuse、metrics→Prometheus（T0.5） ⚠️镜像 tag 待 `docker compose up` 实机核验 |
| Langfuse | LLM 调用链回放 | langfuse/langfuse:3.29.0 + langfuse-worker:3.29.0（docker-compose） | 自托管 docker-compose 先行（T0.5 从简），M2 后可迁集群内 ⚠️tag 待核验。数据面：postgres:16.4-alpine / clickhouse-server:24.12.2-alpine / valkey:8.0-alpine / minio:RELEASE.2025-01-20T14-49-07Z |
| Volcano | 调度层（L0-c） | 不引入 | M4 决策门 go 之后才引入，仓库内不提前放依赖（落地方案 §9）；过门条件见设计文档 §4.5/§7（改善 ≥10% 否则砍） |

## 钉死纪律

1. **单一事实源**：上表是全仓库唯一版本声明处；脚本、CI workflow、文档引用一律指向本表。
2. **双端同源**：本地 WSL2 与 CI（ubuntu-latest）从本表取同一版本；kind/helm/kubectl 的安装脚本两侧共用同一份（aegis-ci 规范）。
3. **改版本 = 单独 PR**：PR 描述写明升级理由、兼容性核验结果与回归结论；kagent 升级须对照 ADR-002 复评口径。
4. **禁跟踪 main/HEAD**：一切组件钉 tag；kagent 明确禁止跟踪 main（落地方案 §9）。
5. **对账**：go.mod 的 client-go/controller-runtime/OPA 等核心依赖版本须与本表对账一致（aegis-go-quality 依赖纪律）。
6. **离线约束**：新增组件须通过"HCS 私域演示可离线安装"评审（`deploy/README.md` §4.1 E3 口径）。
7. **修订留痕**：每次钉死或变更在下表追加一行，禁止静默改版本。

## 修订记录

| 版本 | 日期 | PR | 理由 |
| --- | --- | --- | --- |
| v0.1 初稿（全部 TBD） | 2026-09-23 | — | 落地方案 §11 立即行动第 1 条：W1 安装核验后逐行钉死 |
| v0.2 首轮钉死 | 2026-10-05 | — | T0.2 落地：工具链（kind/kubectl/helm/Go/golangci-lint）+ Chaos Mesh chart 2.7.2 已核验；Prometheus/ArgoCD chart 版本初钉但仓库当前网络不可达、待 `make up` 实机核验；新增机器可读 `deploy/versions.env` |
