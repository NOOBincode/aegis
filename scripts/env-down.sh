#!/usr/bin/env bash
# scripts/env-down.sh —— 逆序销毁环境，幂等可重入（scripts/README.md §4.2；deploy/kind/README.md §4.5）。
# 退出码：0 清理干净；非 0 有残留需人工。
# 用法：env-down.sh [--force]   （--force 跳过确认；默认交互确认，CI 传 --force）
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="env-down"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

CLUSTER_NAME="${AEGIS_CLUSTER_NAME:-aegis}"
FORCE=0
[ "${1:-}" = "--force" ] && FORCE=1

main() {
  info teardown action=begin "component=kind"
  if ! command -v kind >/dev/null 2>&1; then
    warn teardown "error_class=kind_missing" "hint=kind 未安装，视为无集群可清理"
    exit 0
  fi

  if ! cluster_exists "$CLUSTER_NAME"; then
    info teardown action=noop "component=kind" "result=already_clean"
    echo "集群 ${CLUSTER_NAME} 不存在，无需清理（幂等）。"
    exit 0
  fi

  if [ "$FORCE" -ne 1 ]; then
    printf '确认删除 kind 集群 %s 及其全部组件？[y/N] ' "$CLUSTER_NAME"
    read -r ans
    [ "$ans" = "y" ] || [ "$ans" = "Y" ] || { info teardown action=aborted; exit 0; }
  fi

  # 逆序：组件随集群一并删除（kind 集群即边界），删集群即清理。
  kind delete cluster --name "$CLUSTER_NAME" \
    || die 1 teardown delete_fail "kind delete 失败；残留检查: docker ps -a | grep ${CLUSTER_NAME}"
  info teardown action=deleted "component=kind"

  # 残留校验（§4.2：非 0 = 有残留需人工）。
  if cluster_exists "$CLUSTER_NAME"; then
    die 1 teardown residual "集群删除后仍可检出，需人工清理"
  fi
  info teardown action=done "result=clean"
  echo "环境已清理（幂等可重入：再次执行将报 already_clean）。"
}
main "$@"
