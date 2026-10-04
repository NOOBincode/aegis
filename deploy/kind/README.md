# deploy/kind — kind 本地开发集群

> 所属层：L0/L1 运行底座（deploy 子单元）｜ 里程碑：Phase 0（T0.2，W2）建立，T1.9（W8）扩展沙箱 ｜ 设计文档出处：§3.2、§4.4、§7 ｜ 关联不变量：I3、I9 ｜ 关联失败模式：F9、F12

## 1. 设计初衷

kind 集群是 M1–M4 全部"可演示现象"的物理底座：M1 的 OOMKilled 注入诊断、M2 三连演示、M3 场景库与熔断演示都跑在 kind 上（设计文档 §7）。设计初衷三条：

1. **零成本可重建**：个人项目用 kind 而非外部集群，删了重来不花钱（T0.2 判据）。
2. **与 CI 同构**：CI 的 kind-smoke 与本地用同一份 kind 配置与脚本，环境差异最小化（落地方案附录 B-3）。
3. **单集群即完整**：满足 N6——本期不做多集群，CRD 带 `clusterRef` 接缝但环境恒为默认集群（设计文档 §5.1）。

## 2. 职责与任务清单

| # | 职责 | 交付物 |
| --- | --- | --- |
| 1 | kind 集群配置（节点/端口/镜像加速） | 配置文件与创建脚本（由 `scripts/env-up.sh` 编排） |
| 2 | 组件安装清单与顺序 | 本文档 §4.4 |
| 3 | make up 中集群子流程 | 本文档 §4.5 |
| 4 | gVisor RuntimeClass 验证 | `scripts/runsc-smoke.sh` 与降级链（§4.6） |
| 5 | 常见问题排查 | FAQ 表（§4.7） |

## 3. 技术选型与开源包

- kind + docker 为底座；kubectl/helm 客户端版本以 `deploy/versions.md` 钉死为准。
- 组件清单：Chaos Mesh（故障注入）、agent-sandbox（沙箱底座，版本限定 v1.0.x 系列，设计文档 §3.3）、Prometheus、ArgoCD、kagent，全部版本以版本表为准。
- 沙箱内隔离：gVisor（默认，runsc）/ Kata（强隔离场景），经 RuntimeClass 切换（设计文档 §3.3）；沙箱内 kubeconfig 仅 R0 只读权限（§4.4，I3 的沙箱侧表达）。

## 4. 具体设计（不写代码）

### 4.1 集群拓扑（规划默认值，可按机器资源调减）

| 节点 | 数量 | 角色 | 用途 |
| --- | --- | --- | --- |
| control-plane | 1 | 控制面 + 组件宿主 | apiserver、ArgoCD/Prometheus/kagent 等控制类组件 |
| worker | 2 | 负载与沙箱宿主 | 故障注入的负载分散、gVisor RuntimeClass 验证需要真实节点行为 |

默认 1 控制面 + 2 工作节点。内存紧张时可减为 1+1，但控制器干扰类场景（F11/F13 归因验证，T2.9）的演练效果会打折，需在摘要中知悉。

### 4.2 端口映射规划（kind extraPortMappings，规划值可调）

| 宿主端口 | 集群服务 | 用途 |
| --- | --- | --- |
| 6443 | kube-apiserver | kubectl 出口 |
| 30090 | Prometheus | 指标调试 |
| 30300 | Grafana | 闸门核心看板（`deploy/observability/README.md` §4.3） |
| 30443 | ArgoCD | R2 GitOps 通道演示（T2.10） |
| 30880 | kagent UI | Agent 声明与调试入口 |

端口值是规划默认，冲突时改配置即可，但同一仓库只保留一份映射表（本文档）。

### 4.3 容器镜像加速

镜像仓库镜像源与代理变量写入 kind 配置与脚本注释（落地方案 §9：配置写脚本注释，避免网络环境差异卡壳）。CI 环境不假设代理可达，依赖镜像源本身可达性。

### 4.4 组件安装清单与顺序

顺序（编号步骤，每组件等待 Ready 后才进入下一个）：

1. kind 集群本体（§4.1/§4.2 配置）。
2. Chaos Mesh——故障注入是"环境能力"，M1 验收与 M3 场景库（T3.2）都依赖它。
3. agent-sandbox（含 gVisor RuntimeClass 前提）——沙箱是 F9 防线的环境底座（T1.9）。
4. Prometheus——先装观测，后续组件的健康状况才有呈现面（`deploy/observability/`）。
5. ArgoCD——R2 强制 PR 通道（ADR-001，T2.10）。
6. kagent——Agent 运行时（T0.4，ADR-002 边界：仅配置层二开）。
7.（M1 起）`deploy/observability/` 的 OTel collector。
8.（M2 起）`crds/` → controller → gatekeeper → GuardrailPolicy default 实例 + Rego bundle（`policies/`）。

顺序理由：先基础设施能力（注入、沙箱），再观测，再 GitOps 与 Agent 运行时，最后自研闸门——契约（crds/）永远先于它的消费者。

### 4.5 make up 中 kind 子流程（文字步骤）

1. 读取版本表中的 kind/kubectl 版本要求，校验本机二进制。
2. 幂等检查：集群不存在则按 §4.1/§4.2 配置创建；已存在则校验节点数与端口映射，不符则提示先 make down。
3. 注入镜像加速配置。
4. 按 §4.4 顺序安装组件。
5. 执行 runsc 冒烟（§4.6）。
6. 输出健康摘要（组件/版本/状态/端口）。

### 4.6 gVisor RuntimeClass 验证

- **验证方式**：在 kind 集群内跑 runsc 冒烟 Pod（`scripts/runsc-smoke.sh`），预期按 gVisor RuntimeClass 调度成功且容器正常退出；结论作为 W2 出口自检的输入（落地方案 §9）。
- **平台策略**：WSL2 是真实 Linux 内核，runsc 的 ptrace 平台一般可用，作兜底；若 `/dev/kvm` 可见（Windows 11 已开 WSL2 嵌套虚拟化）可启用 KVM 平台（性能更好）；两者皆不可用，则备选 Linux 云主机/旧机器专供沙箱测试，日常开发不受影响（落地方案 §10 应对）。
- **复检时机**：W2 出口自检必跑；T1.9（沙箱集成，W8）前复检一次。

### 4.7 常见问题表（FAQ）

| 现象 | 可能原因 | 排查与处置 |
| --- | --- | --- |
| 镜像拉取反复失败 | 网络受限、镜像源未生效 | 检查 §4.3 加速配置是否注入；确认代理变量；CI 环境换用可达镜像源 |
| 组件 Pod 起不来/崩溃 | 内存不足（WSL2 未划内存上限） | `.wslconfig` 显式划内存（≥16GB 底线）；按 §4.1 减为 1+1 节点 |
| kind create 超时 | docker 资源不足或 WSL2 网络异常 | 确认 docker 可用与磁盘余量；WSL2 网络问题先 `wsl --shutdown` 重启 |
| runsc 冒烟失败 | 平台不支持或嵌套虚拟化未开 | 按 §4.6 降级链：ptrace 兜底 → KVM → 备选 Linux 主机 |
| `/mnt/c` 上构建极慢 | 跨文件系统性能与 inotify 限制 | 仓库移入 WSL 原生文件系统 `~/`（落地方案 §9） |

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 集群创建失败（docker 资源不足） | kind 报错与前置检查 | 按 FAQ 调内存或减节点后重试 | fail-fast：环境坏在起点 |
| 组件 Pod 不就绪 | 就绪探针轮询超时 | 有限重试后失败，输出 describe 诊断信息 | 整体 fail（I10：失败必须响） |
| 镜像拉取失败 | ImagePullBackOff/超时 | 加速配置指引，重试后仍失败即 fail | fail-fast |
| runsc 冒烟失败 | 冒烟脚本退出码 | 按 §4.6 降级链处置 | **显式降级**：沙箱不可用是环境能力缺失，对应 F9 防线打折，绝不允许"假装可用"——健康摘要必须标出沙箱不可用 |
| apiserver 偶发不可达 | 探针重试后仍失败 | 判环境不可用，输出诊断 | fail-fast |
| 实装组件版本与版本表漂移 | 安装时逐组件比对 | 拒绝继续（纪律见 `deploy/README.md` §4.2） | fail-closed：版本漂移不可复现 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **结构化日志字段**：ts、level、script_name、step、component、version、action、result、duration_ms、error_class、hint。
- **指标**：`aegis_env_up_component_total{component, result}`、`aegis_runsc_smoke_up`（0/1）；集群运行时指标走 Prometheus（`deploy/observability/README.md`）。
- **审计留痕点**：每次环境重建的健康摘要归档 logs/（含组件版本快照），与版本表的 PR 历史互证；runsc 冒烟结论随摘要留痕，作为 T1.9 的前置证据。

## 7. 依赖方向与模块边界

- **谁调我**：`scripts/env-up.sh`（make up 的编排主体）、CI 同源脚本、开发者手动调试。
- **我调谁**：docker/kind/kubectl/helm 外部二进制；`deploy/versions.md`；`deploy/observability/`（其安装步骤由本层流程按序编排）。
- **禁止依赖谁**：不依赖 `internal/` 任何逻辑；组件 manifest/helm 值不内嵌版本号（一律版本表注入）；不向脚本或配置写入任何真实集群凭据。

## 8. 测试策略与红队用例

- **W2 出口自检**：make up 全绿 + runsc 冒烟通过（Phase 0 出口标准组成部分，落地方案 §3）。
- **幂等测试**：make up 连续两次执行结果一致。
- **顺序依赖冒烟**：组件安装顺序错乱应被前置依赖检测拦下（如未装 Prometheus 前 Grafana 就绪检查必失败）。
- **红队靶场支撑**：M2 演示②（恶意日志注入）与红队检查点 #2 的越权/重放用例在本集群执行；Chaos Mesh 是 M3 场景库（T3.2）与熔断演示（T3.5）的注入工具；沙箱内任何写调用被凭证/网络双层拒绝是 T1.9 完成判据，也是 F9 红队用例的常态验证。
