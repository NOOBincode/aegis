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
COMPONENTS=("$@")
[ "${#COMPONENTS[@]}" -eq 0 ] && COMPONENTS=("${DEFAULT_COMPONENTS[@]}")

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
cluster_up() {
  if cluster_exists "$CLUSTER_NAME"; then
    info cluster action=exists "component=kind" "hint=已存在，跳过创建（如需变更端口/节点先 make down）"
    return 0
  fi
  local rendered; rendered="$(mktemp)"
  sed "s#__REGISTRY_MIRROR_DOCKER_IO__#${AEGIS_REGISTRY_MIRROR_DOCKER_IO:-https://docker.m.daocloud.io}#g" \
    "$KIND_CONFIG" > "$rendered"
  info cluster action=create "component=kind" "version=${AEGIS_KIND_NODE_IMAGE:-default}"
  kind create cluster --name "$CLUSTER_NAME" --config "$rendered" \
    ${AEGIS_KIND_NODE_IMAGE:+--image "${AEGIS_KIND_NODE_IMAGE}"} \
    || die 1 cluster create_fail "kind create 失败，诊断见 deploy/kind/README.md §4.7"
  rm -f "$rendered"
  kubectl cluster-info --context "kind-${CLUSTER_NAME}" >/dev/null \
    || die 4 cluster apiserver_unreachable "apiserver 不可达"
  info cluster action=ready "component=kind"
}

# ---- 3. 组件安装（helm upgrade --install 幂等；逐个等 Ready）----
helm_upsert() { # helm_upsert <release> <chart> <namespace> <chart_version> [extra --set ...]
  local release="$1" chart="$2" ns="$3" ver="$4"; shift 4
  info install "component=${release}" "version=${ver}" action=helm_upgrade
  helm upgrade --install "$release" "$chart" \
    --namespace "$ns" --create-namespace \
    --version "$ver" --wait --timeout 5m "$@" \
    || die 3 "install_${release}" helm_fail "helm 安装 ${release} 失败；查 ${ns} 命名空间 Pod 事件"
}

install_chaos_mesh() {
  helm repo add chaos-mesh https://charts.chaos-mesh.org >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1 || true
  helm_upsert chaos-mesh chaos-mesh/chaos-mesh chaos-mesh "${AEGIS_CHAOS_MESH_CHART_VERSION:?}" \
    --set chaosDaemon.runtime=containerd
  wait_deploy chaos-mesh chaos-controller-manager 300
}

install_prometheus() {
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1 || true
  helm_upsert prometheus prometheus-community/kube-prometheus-stack "$OBS_NS" "${AEGIS_KUBE_PROMETHEUS_STACK_CHART_VERSION:?}" \
    --set grafana.service.type=NodePort --set grafana.service.nodePort=30300 \
    --set prometheus.service.type=NodePort --set prometheus.service.nodePort=30090
  wait_deploy "$OBS_NS" prometheus-grafana 300
}

install_argocd() {
  helm repo add argo https://argoproj.github.io/argo-helm >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1 || true
  helm_upsert argocd argo/argo-cd argocd "${AEGIS_ARGO_CD_CHART_VERSION:?}" \
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
  want chaos-mesh && install_chaos_mesh
  want prometheus && install_prometheus
  want argocd     && install_argocd
  runsc_smoke
  summary
  info done "duration_ms=$(( (SECONDS - t0) * 1000 ))"
}
main "$@"
