# deploy — 部署与运行环境总述

> 所属层：跨层（L0–L4 各层进程的统一部署载体）｜ 里程碑：Phase 0（W1–W2 建立，随后全程维护）｜ 设计文档出处：§3.2、§4、§7 ｜ 关联不变量：I10 ｜ 关联失败模式：F9、F14

## 1. 设计初衷

deploy/ 是"环境即代码"的落点，服务三个初衷：

1. **一键可重建**：个人项目没有运维团队，环境必须删除后从零重建且结果一致——这是落地方案 T0.2 的完成判据（"make up 从零到全绿；删集群重来可复现"）。
2. **本地与 CI 同源**：所有安装脚本只有一份，本地 WSL2 与 CI（ubuntu-latest）共用；版本号不散落在脚本与 workflow 里，全部收敛到 `deploy/versions.md`（落地方案 §9）。
3. **部署不承载智能**：deploy/ 只做装配编排与配置，不放任何业务规则——风险分级、审批、回滚判断一律在 `internal/`（轻型 DDD 铁律 1、3 的部署侧表达）；部署层的失败语义向 I10 看齐：失败必须响，绝不静默半绿。

## 2. 职责与任务清单

| # | 职责 | 交付物 | 对应任务 |
| --- | --- | --- | --- |
| 1 | 环境分层管理与定义 | 本文档 §4.1 三环境模型 | T0.2、落地方案 §9 |
| 2 | 版本钉死治理 | `deploy/versions.md`（单一事实源） | 落地方案 §9、§11 |
| 3 | kind 本地开发集群 | `deploy/kind/` | T0.2 |
| 4 | 观测基座 | `deploy/observability/` | T0.5 |
| 5 | 自研组件部署单元 | helm chart（M2 起随 gatekeeper/controller 入库） | T2.1 及后续 |
| 6 | 未来 HCS 私域演示环境预留 | 选型验收口径（§4.1 E3） | 设计文档 §1.5、N6 |

## 3. 技术选型与开源包

- 外部二进制仅四个：docker、kind、kubectl、helm，版本以 `deploy/versions.md` 钉死为准。
- 组件（Chaos Mesh、ArgoCD、Prometheus、kagent、agent-sandbox 等）一律经 helm 或官方 manifest 安装，版本同样只从版本表取。
- 跨平台纪律（落地方案附录 B）：自动化一律 Makefile + `scripts/*.sh`（bash），PowerShell/BAT 不进关键路径；`.gitattributes` 钉 `*.sh`/`Makefile`/`*.yml` 为 LF；禁 CGO 等约定在代码侧由 aegis-go-quality 约束，部署侧只保证脚本与配置不引入平台假定。
- CI 是唯一裁决环境：本地过了 CI 没过，一律以 CI 为准修本地（附录 B-3）。

## 4. 具体设计（不写代码）

### 4.1 环境分层

| 环境 | 载体 | 用途 | 裁决地位 |
| --- | --- | --- | --- |
| E1 本地 kind 开发环境 | WSL2 Ubuntu + Docker | 日常开发、M1–M3 全部演示与红队靶场 | 开发入口，非裁决环境 |
| E2 CI kind 冒烟环境 | GitHub Actions ubuntu-latest | PR 门禁：起集群 → apply crds → controller 状态机冒烟 →（M2 起）三连演示脚本 | 唯一裁决环境 |
| E3 未来 HCS 私域演示环境 | 离线/私域基础设施 | 私域 vLLM 通道（设计文档 §4.3）、等保审计叙事素材（§1.5） | 本期不建设，仅作选型验收场景 |

E1 资源底线：内存 ≥16GB（kind 单集群 + Prometheus + ArgoCD + kagent + Langfuse 同时跑，落地方案 §9）；仓库放 WSL 原生文件系统（`~/`），禁止在 `/mnt/c` 上跑 kind 与构建（跨文件系统性能与 inotify 限制，落地方案 §9）。

E3 约定两条：完全复用 E1/E2 的同一份脚本与版本表；凡"必须公网才能安装或拉取"的组件选型，在 `deploy/versions.md` 评审时直接打回。理由：HCS 私域是差异化叙事场景（设计文档 §1.5 冷静剂 3），不能到演示前夜才发现公网硬依赖。

### 4.2 versions.md 的治理地位

- **单一事实源**：kind/kubectl/helm/Go/Python/golangci-lint/kagent/agent-sandbox/Chaos Mesh/ArgoCD/Prometheus/OTel collector/Langfuse 的版本只在版本表一处声明。
- **双端同源**：本地脚本与 CI 从同一份表取版本，kind/helm/kubectl 安装脚本在两侧共用同一份（aegis-ci 规范）。
- **改版本 = 单独 PR**：PR 描述写明升级理由与兼容性核验结果；kagent 升级须对照 ADR-002 的复评口径。
- **对账纪律**：go.mod 中 client-go/controller-runtime/OPA 等核心依赖版本须与版本表对账一致（aegis-go-quality 依赖纪律）。

### 4.3 make up / make down 的目标流程

make up 的文字步骤（实现在 `scripts/env-up.sh`，见 `scripts/README.md`）：

1. 前置检查：docker 可用、内存余量达标、版本表可读。
2. 幂等建集群：kind 集群已存在则校验拓扑后跳过或提示先 make down。
3. 注入镜像加速配置（配置位写在 `deploy/kind/` 脚本注释，落地方案 §9）。
4. 按组件清单顺序安装（顺序与理由见 `deploy/kind/README.md` §4），每个组件等待 Ready 后才进入下一个。
5. 执行 runsc 冒烟（`scripts/runsc-smoke.sh`，W2 出口自检项）。
6. 输出健康摘要：组件/版本/状态/端口一览；任一失败即整体失败，不存在"半绿"。

make down：逆序停止并删除集群与派生资源，可重复执行（幂等）。

### 4.4 目录布局

`deploy/` 下设 `kind/`（本地集群，见 `deploy/kind/README.md`）、`observability/`（观测基座，见 `deploy/observability/README.md`）、`versions.md`（版本表）；helm chart 作为自研组件的部署单元在本目录组织，M2 起随闸门组件入库。Phase 0 出口标准（落地方案 §3）：make up 一键起 kind + Chaos Mesh + ArgoCD + Prometheus + kagent，CI 绿，ADR-001/002 落盘。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 前置依赖缺失（docker 未启动、内存不足、二进制缺失） | 脚本启动前置检查 | 立即退出，输出补齐指引 | fail-fast：半套环境是假象 |
| kind 集群已存在且拓扑不符 | 集群名与节点数比对 | 提示先 make down，禁止静默重建 | 幂等防误删；环境变化必须显式 |
| 组件安装超时或失败 | 就绪探针轮询超时 | 有限重试后退订，输出该组件诊断信息 | 整体 fail：I10 要求失败必须响，不许"半绿说成绿" |
| 镜像拉取失败 | 拉取错误码/超时 | 提示镜像加速与代理配置（FAQ 见 `deploy/kind/README.md` §4.6） | 重试后仍失败即 fail |
| runsc 冒烟失败 | 冒烟脚本退出码 | 按 `deploy/kind/README.md` 的降级链处置并在摘要显式标注 | 降级：沙箱不可用是环境能力缺失而非放行判断问题，但绝不假装可用 |
| 实装版本与版本表漂移 | 安装时逐组件比对 versions.md | 拒绝继续，要求先改版本表走 PR | fail-closed：版本漂移 = 不可复现，直接关联 F14 的不可复现失效 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **结构化日志字段**（全部部署脚本统一）：ts、level、script_name、step、component、version、action、result、duration_ms、error_class、hint。
- **指标**：`aegis_env_up_component_total{component, result}`（组件安装结果计数）、`aegis_env_up_duration_seconds`（整体耗时直方图）、`aegis_runsc_smoke_up`（冒烟结果 0/1）。
- **Trace**：部署流程操作的是外部二进制，MVP 期不进 OTel 管道，以结构化日志为事实源。
- **审计留痕点**：版本表每次变更走单独 PR（git 历史即审计链）；每次 make up 的健康摘要归档到本地 logs/，环境重建事件可追溯。

## 7. 依赖方向与模块边界

- **谁调我**：根 Makefile 与 `scripts/*.sh`（Makefile 只做薄封装，见 `scripts/README.md` §4.3）；CI 调用与本地完全同一份脚本。
- **我调谁**：外部二进制（docker/kind/kubectl/helm）；`deploy/versions.md`；deploy/ 内部子目录（`kind/`、`observability/`）。
- **禁止依赖谁**：不 import、不内嵌任何 `internal/` 与 `mcp-servers/` 业务逻辑；禁止在 deploy 配置里硬编码版本号（一律引用版本表）；反向地，代码侧也不得依赖 deploy/ 才能运行（领域纯净铁律的镜像表达）；deploy 不得引入任何需要真实集群凭据的流程（CI 规范：kind 是唯一集群）。

## 8. 测试策略与红队用例

- **可复现测试**：make down && make up 从零到全绿（T0.2 完成判据）；连续执行两次 make up 验证幂等。
- **CI 同源测试**：kind-smoke.yml 与本地共用脚本，保证裁决一致（附录 B-3/B-4）。
- **红队支撑**：deploy 层本身不承担红队用例，但提供全部靶场——Chaos Mesh 故障注入（M3 场景库载体，T3.2）、恶意日志注入环境（红队检查点 #1 与 M2 演示②依赖）、审批绕过与重放测试的 kind 底座。红队检查点 #2（M2 末，项目准入门槛，设计文档 §9）的全部用例必须先在本层环境可重复跑通，再走 `scripts/redteam/` 脚本化归档。
