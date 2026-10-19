#!/usr/bin/env bash
# scripts/env-up.sh —— make up 主体（scripts/README.md §4.2；deploy/kind/README.md §4.5）。
# 流程：前置检查 → 幂等建集群 → 按序装组件 → runsc 冒烟 → 健康摘要。
# 退出码：0 全绿；2 前置缺失；3 组件超时；4 环境不可用；1 其他失败。
# 幂等：连续执行两次结果一致（T0.2 完成判据）。
# 用法：env-up.sh [组件白名单...]  —— 缺省装全部 Phase 0 组件（chaos-mesh prometheus argocd）。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="env-up"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

CLUSTER_NAME="${AEGIS_CLUSTER_NAME:-aegis}"
KIND_CONFIG="${AEGIS_ROOT}/deploy/kind/kind-config.yaml"
OBS_NS="observability"

# 默认 Phase 0 组件序列（deploy/kind/README.md §4.4；agent-sandbox=T1.9 不在本骨架内）。
# 含 kagent：Phase 0 出口标准要求 make up 一键起 kind+Chaos+ArgoCD+Prometheus+kagent（落地方案 §3）。
# metrics-server：T1.2 get_resource_metrics 的 metrics.k8s.io 数据源，缺它该工具必然失败。
DEFAULT_COMPONENTS=(chaos-mesh prometheus argocd kagent metrics-server)
CLUSTER_ONLY=0
COMPONENTS=()
for a in "$@"; do
  if [ "$a" = "--cluster-only" ]; then CLUSTER_ONLY=1; else COMPONENTS+=("$a"); fi
done
# 缺省装全部 Phase 0 组件；--cluster-only 时只起集群（CI kind-smoke 骨架用，组件安装属完整环境）。
if [ "$CLUSTER_ONLY" -eq 0 ] && [ "${#COMPONENTS[@]}" -eq 0 ]; then COMPONENTS=("${DEFAULT_COMPONENTS[@]}"); fi

want() { local c; for c in "${COMPONENTS[@]}"; do [ "$c" = "$1" ] && return 0; done; return 1; }

# ---- 1. 前置检查（退出码 2）----
preflight() {
  info preflight "action=begin"
  # 先查宿主 inotify：WSL2 默认 128 会让 kube-proxy/CoreDNS 报 too many open files，
  # 整个集群假死且无法由集群内组件自愈（必须先于 docker/kind 检查）。
  require_host_inotify
  require_docker
  require_bin kind    "${AEGIS_KIND_VERSION:-}"      "见 deploy/versions.md"
  require_bin kubectl "${AEGIS_KUBECTL_VERSION:-}"   "见 deploy/versions.md"
  require_bin helm    "${AEGIS_HELM_VERSION:-}"      "见 deploy/versions.md"
  [ -f "$KIND_CONFIG" ] || die 2 preflight missing_config "缺 ${KIND_CONFIG}"
  info preflight "action=done"
}

# ---- 2. 幂等建集群 ----
# 节点镜像确保：docker.io 不可达时经镜像源拉取并 retag（deploy/kind/README.md §4.3/§4.7）。
ensure_node_image() {
  local img="${AEGIS_KIND_NODE_IMAGE:-}"
  [ -n "$img" ] || return 0
  if docker image inspect "$img" >/dev/null 2>&1; then
    info cluster action=node_image_present "image=${img}"; return 0
  fi
  local mirror_host="${AEGIS_REGISTRY_MIRROR_DOCKER_IO#https://}"
  mirror_host="${mirror_host#http://}"; mirror_host="${mirror_host%/}"
  local mirror_img="${mirror_host}/${img}"
  info cluster action=node_image_pull "image=${img}" "via_mirror=${mirror_host}"
  { docker pull "$mirror_img" >/dev/null 2>&1 && docker tag "$mirror_img" "$img"; } \
    || docker pull "$img" >/dev/null 2>&1 \
    || die 2 cluster node_image_pull_fail "节点镜像拉取失败；配置镜像源/代理后重试（§4.3）"
}

cluster_up() {
  if cluster_exists "$CLUSTER_NAME"; then
    local want_nodes="${AEGIS_EXPECTED_NODES:-3}"
    local got_nodes; got_nodes="$(kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')"
    if [ "$got_nodes" != "$want_nodes" ]; then
      die 4 cluster topology_mismatch "已有集群节点数=${got_nodes}，期望=${want_nodes}（deploy/kind/README.md §4.1）。如需变更先 make down"
    fi
    info cluster action=exists "component=kind" "hint=已存在，节点数=${got_nodes}；如需变更端口/节点先 make down"
    return 0
  fi
  ensure_node_image
  info cluster action=create "component=kind" "version=${AEGIS_KIND_NODE_IMAGE:-default}"
  kind create cluster --name "$CLUSTER_NAME" --config "$KIND_CONFIG" \
    ${AEGIS_KIND_NODE_IMAGE:+--image "${AEGIS_KIND_NODE_IMAGE}"} \
    || die 1 cluster create_fail "kind create 失败，诊断见 deploy/kind/README.md §4.7"
  kubectl cluster-info --context "kind-${CLUSTER_NAME}" >/dev/null \
    || die 4 cluster apiserver_unreachable "apiserver 不可达"
  info cluster action=ready "component=kind"
}

# ---- 3. 组件安装（helm upgrade --install 幂等；逐个等 Ready）----
helm_upsert() { # helm_upsert <release> <chart> <namespace> <chart_version> [extra --set ...]
  local release="$1" chart="$2" ns="$3" ver="$4"; shift 4
  info install "component=${release}" "version=${ver}" action=helm_upgrade
  # --wait 超时放宽到 10m：本网络镜像拉取慢（DaoCloud 镜像源），首次安装要拉全量镜像。
  helm upgrade --install "$release" "$chart" \
    --namespace "$ns" --create-namespace \
    --version "$ver" --wait --timeout 10m "$@" \
    || die 3 "install_${release}" helm_fail "helm 安装 ${release} 失败；查 ${ns} 命名空间 Pod 事件"
}

install_chaos_mesh() {
  # chaos-mesh 未发布到 ghcr，用其官方 repo（本网络可达）。
  helm repo add chaos-mesh https://charts.chaos-mesh.org >/dev/null 2>&1 || true
  helm repo update chaos-mesh >/dev/null 2>&1 || true
  # 本地故障注入底座从简：controller-manager 单副本（默认 3 副本的 leader-election 在小集群上会翻滚卡住）。
  # 注意 chart 2.7.2 的值名是 controllerManager.replicaCount；早先写 .replicas 是无效值，
  # 实装仍为 3 副本，正是 inotify/fd 压力的来源之一（本次修复）。
  helm_upsert chaos-mesh "${AEGIS_CHAOS_MESH_CHART:?}" chaos-mesh "${AEGIS_CHAOS_MESH_CHART_VERSION:?}" \
    --set chaosDaemon.runtime=containerd \
    --set chaosDaemon.socketPath=/run/containerd/containerd.sock \
    --set controllerManager.replicaCount=1
  wait_deploy chaos-mesh chaos-controller-manager 300
  # 就绪检查之外再做实装断言：避免"value 写错但 helm 成功"再次发生。
  local got; got="$(kubectl -n chaos-mesh get deploy chaos-controller-manager -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
  [ "$got" = "1" ] || die 3 chaos_replicas "chaos-controller-manager 期望 replicas=1，实装=${got:-unknown}"
}

install_prometheus() {
  # OCI（ghcr）安装：本网络 *.github.io Pages 被墙，ghcr OCI 已核验可达。
  helm_upsert prometheus "${AEGIS_PROMETHEUS_CHART:?}" "$OBS_NS" "${AEGIS_KUBE_PROMETHEUS_STACK_CHART_VERSION:?}" \
    --set grafana.service.type=NodePort --set grafana.service.nodePort=30300 \
    --set prometheus.service.type=NodePort --set prometheus.service.nodePort=30090
  wait_deploy "$OBS_NS" prometheus-grafana 300
}

install_argocd() {
  # OCI（ghcr）安装，理由同上。
  helm_upsert argocd "${AEGIS_ARGO_CD_CHART:?}" argocd "${AEGIS_ARGO_CD_CHART_VERSION:?}" \
    --set server.service.type=NodePort --set server.service.nodePortHttps=30443
  wait_deploy argocd argocd-server 300
}

install_metrics_server() {
  # metrics-server：T1.2 get_resource_metrics 的数据源。bitnami chart 走 OCI 时 docker.io
  # 不可达，改用 charts.bitnami.com 的 tgz + SHA256 钉死（fetch_chart）；镜像用官方
  # registry.k8s.io/metrics-server（kind 的 registry.k8s.io containerd 镜像源已核验）。
  fetch_chart "${AEGIS_METRICS_SERVER_CHART_URL:?}" "${AEGIS_METRICS_SERVER_CHART_SHA256:?}" metrics-server
  # bitnami chart 只做部署骨架；镜像换官方 metrics-server（bitnami/docker.io 在本网络 403）。
  # 官方镜像是 distroless 且默认在 CWD 生成自签证书，kind 下给它可写 rootfs + root 运行；
  # 生产环境应改成自定义 manifest + 可写 emptyDir/workingDir 的非 root 方案（本环境仅指标只读）。
  local image="${AEGIS_METRICS_SERVER_IMAGE:?}" registry rest repo tag
  registry="${image%%/*}"; rest="${image#*/}"; repo="${rest%:*}"; tag="${image##*:}"
  info install component=metrics-server "version=${AEGIS_METRICS_SERVER_CHART_VERSION:?}" "image=${image}" action=helm_upgrade
  helm upgrade --install metrics-server "${AEGIS_FETCHED_CHART}" \
    --namespace kube-system \
    --set image.registry="$registry" \
    --set image.repository="$repo" \
    --set image.tag="$tag" \
    --set image.pullPolicy=IfNotPresent \
    --set global.security.allowInsecureImages=true \
    --set 'command[0]=/metrics-server' \
    --set containerSecurityContext.readOnlyRootFilesystem=false \
    --set containerSecurityContext.runAsNonRoot=false \
    --set containerSecurityContext.runAsUser=0 \
    --set apiService.create=true \
    --set 'extraArgs[0]=--kubelet-insecure-tls=true' \
    --set 'extraArgs[1]=--kubelet-preferred-address-types=InternalIP' \
    --wait --timeout 10m \
    || die 3 install_metrics-server helm_fail "metrics-server 安装失败；查 kube-system Pod 事件"
  wait_deploy kube-system metrics-server 300
}

install_kagent() {
  # T0.4：kagent 经 OCI（ghcr）安装；先 CRDs 后 controller（契约先于消费者）。
  info install component=kagent-crds "version=${AEGIS_KAGENT_CHART_VERSION:?}" action=helm_upgrade
  helm upgrade --install kagent-crds "${AEGIS_KAGENT_CRDS_CHART:?}" \
    --namespace kagent --create-namespace --version "${AEGIS_KAGENT_CHART_VERSION:?}" \
    || die 3 install_kagent-crds helm_fail "kagent-crds 安装失败"
  # 安全/资源收口：
  #  - kagent-tools 自带 cluster-admin（*/*/*）+ shell/k8s_delete 等写工具，本项目的 M1 只用
  #    scan/k8s-read 两个只读 server；不关它等于给全集群留一条绕过闸门的写路径。
  #  - kagent chart 默认还会起 10 个 demo agent + grafana-mcp，纯增 inotify/fd 压力。
  # 这里显式关掉；要启用某个 demo agent 时单独加 --set，不默认全开。
  helm_upsert kagent "${AEGIS_KAGENT_CHART:?}" kagent "${AEGIS_KAGENT_CHART_VERSION:?}" \
    --set providers.default=openAI \
    --set providers.openAI.apiKey=placeholder-not-for-prod \
    --set kagent-tools.enabled=false \
    --set grafana-mcp.enabled=false \
    --set k8s-agent.enabled=false \
    --set kgateway-agent.enabled=false \
    --set istio-agent.enabled=false \
    --set promql-agent.enabled=false \
    --set observability-agent.enabled=false \
    --set argo-rollouts-agent.enabled=false \
    --set helm-agent.enabled=false \
    --set cilium-policy-agent.enabled=false \
    --set cilium-manager-agent.enabled=false \
    --set cilium-debug-agent.enabled=false
  # deployment 名是 kagent-controller，不是 kagent；失败必须响（I10），不再 warn 吞掉。
  wait_deploy kagent kagent-controller 300
  # 实装断言：kagent-tools 必须不存在，否则 cluster-admin 暴露面仍在。
  if kubectl -n kagent get clusterrolebinding kagent-tools-cluster-admin-rolebinding >/dev/null 2>&1; then
    die 3 kagent_tools_exposed "kagent-tools ClusterRoleBinding 仍存在；显式执行 kubectl delete clusterrolebinding kagent-tools-cluster-admin-rolebinding 后重试"
  fi
  # 注意：真实 API key 不走 helm --set（会进 Helm release secret/命令行）；只经
  # scripts/ai-up.sh 写 kagent/aegis-llm Secret，ModelConfig 只做 secret 引用。
}

apply_crds() {
  info apply component=crds action=kubectl_apply
  kubectl apply -f "${AEGIS_ROOT}/crds/" >/dev/null 2>&1 \
    || die 3 crds apply_fail "crds/ apply 失败；检查 CRD schema 与 apiserver"
  info apply component=crds result=ok
}

# ---- 4. runsc 冒烟（显式降级：沙箱是环境能力，不可用必须可见，绝不假装可用）----
runsc_smoke() {
  if [ -x "${AEGIS_ROOT}/scripts/runsc-smoke.sh" ]; then
    if "${AEGIS_ROOT}/scripts/runsc-smoke.sh" "$CLUSTER_NAME"; then
      info runsc result=pass
    else
      # 不 fail：按降级链记录（deploy/kind/README.md §4.6 / §5），健康摘要中显式标注。
      warn runsc "error_class=sandbox_unavailable" "hint=gVisor 不可用，F9 防线打折；见 §4.6 降级链"
      RUNSC_DEGRADED=1
    fi
  else
    warn runsc "error_class=smoke_script_missing" "hint=scripts/runsc-smoke.sh 缺失"
    RUNSC_DEGRADED=1
  fi
}

# ---- 5. 健康摘要（不只看 runsc：kube-system/kagent 等核心命名空间也必须全 Ready）----
summary() {
  info summary action=begin
  echo "==================== aegis 环境健康摘要 ===================="
  kubectl get nodes -o wide 2>/dev/null || true
  echo "------------------------------------------------------------"
  kubectl get pods -A 2>/dev/null | grep -E "kube-system|chaos-mesh|${OBS_NS}|argocd|kagent" || true
  echo "------------------------------------------------------------"
  helm list -A 2>/dev/null || true
  echo "------------------------------------------------------------"
  echo "端口映射: Prometheus=http://localhost:30090  Grafana=http://localhost:30300  ArgoCD=https://localhost:30443  kagent UI=http://localhost:30880"
  echo "runsc/gVisor: $([ "${RUNSC_DEGRADED:-0}" = 1 ] && echo '不可用(降级,见 §4.6)' || echo '可用')"

  # 核心控制面永远检查：CoreDNS/kube-proxy 任一不 Ready，集群就是"假活"。
  require_namespace_ready kube-system kube-system
  want chaos-mesh     && require_namespace_ready chaos-mesh chaos-mesh
  want prometheus     && require_namespace_ready "$OBS_NS" prometheus
  want argocd         && require_namespace_ready argocd argocd
  want kagent         && require_namespace_ready kagent kagent
  # metrics-server 在 kube-system 内，已随上面的 kube-system 检查覆盖。

  echo "============================================================"
  info summary action=done "result=$([ "${RUNSC_DEGRADED:-0}" = 1 ] && echo degraded || echo green)"
}

main() {
  local t0=$SECONDS
  preflight
  cluster_up
  if [ "$CLUSTER_ONLY" -eq 1 ]; then
    # CI kind-smoke 骨架路径：只验证集群起得来、节点就绪；CRD/状态机冒烟 M1 起接入（workflows README §4）。
    kubectl wait --for=condition=Ready nodes --all --timeout=120s >/dev/null \
      || die 3 node_ready timeout "kind 节点 120s 内未 Ready"
    info done "mode=cluster-only" "result=green" "duration_ms=$(( (SECONDS - t0) * 1000 ))"
    echo "cluster-only 冒烟通过：$(kubectl get nodes --no-headers 2>/dev/null | wc -l) 节点 Ready"
    return 0
  fi
  want chaos-mesh    && install_chaos_mesh
  want prometheus    && install_prometheus
  want argocd        && install_argocd
  want kagent        && install_kagent
  want metrics-server && install_metrics_server
  apply_crds
  runsc_smoke
  summary
  info done "duration_ms=$(( (SECONDS - t0) * 1000 ))"
}
main "$@"
