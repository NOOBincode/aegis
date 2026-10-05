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

# 默认 Phase 0 组件序列（deploy/kind/README.md §4.4；kagent=T0.4、agent-sandbox=T1.9 不在本骨架内）。
DEFAULT_COMPONENTS=(chaos-mesh prometheus argocd)
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
    info cluster action=exists "component=kind" "hint=已存在，跳过创建（如需变更端口/节点先 make down）"
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
  helm_upsert chaos-mesh "${AEGIS_CHAOS_MESH_CHART:?}" chaos-mesh "${AEGIS_CHAOS_MESH_CHART_VERSION:?}" \
    --set chaosDaemon.runtime=containerd \
    --set chaosDaemon.socketPath=/run/containerd/containerd.sock \
    --set controllerManager.replicas=1
  wait_deploy chaos-mesh chaos-controller-manager 300
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

# ---- 5. 健康摘要 ----
summary() {
  info summary action=begin
  echo "==================== aegis 环境健康摘要 ===================="
  kubectl get nodes -o wide 2>/dev/null || true
  echo "------------------------------------------------------------"
  kubectl get pods -A 2>/dev/null | grep -E "chaos-mesh|${OBS_NS}|argocd" || true
  echo "------------------------------------------------------------"
  echo "端口映射: Prometheus=http://localhost:30090  Grafana=http://localhost:30300  ArgoCD=https://localhost:30443  kagent=http://localhost:30880(待 T0.4)"
  echo "runsc/gVisor: $([ "${RUNSC_DEGRADED:-0}" = 1 ] && echo '不可用(降级,见 §4.6)' || echo '可用')"
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
  want chaos-mesh && install_chaos_mesh
  want prometheus && install_prometheus
  want argocd     && install_argocd
  runsc_smoke
  summary
  info done "duration_ms=$(( (SECONDS - t0) * 1000 ))"
}
main "$@"
