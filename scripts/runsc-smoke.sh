#!/usr/bin/env bash
# scripts/runsc-smoke.sh —— gVisor RuntimeClass 冒烟（deploy/kind/README.md §4.6；W2 出口自检）。
# 退出码：0 通过；非 0 = 沙箱不可用，调用方按降级链显式标注（绝不假装可用）。
# 平台策略：/dev/kvm 可见 → KVM 平台；否则 ptrace 兜底（WSL2 真实内核一般可用）。
# 用法：runsc-smoke.sh [集群名]
#
# 说明（诚实边界）：在 kind 节点（容器）内落地 runsc 需要向各节点容器写入 runsc 二进制并配置
# containerd，属 best-effort。任一环节失败即非零退出，由 env-up 标记降级——完整沙箱集成是 T1.9（W8）。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="runsc-smoke"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"

CLUSTER_NAME="${1:-aegis}"
RUNTIME_CLASS="gvisor"
SMOKE_NS="aegis-sandbox-smoke"

# 平台结论（ptrace / kvm）。
platform() { [ -e /dev/kvm ] && echo "kvm" || echo "ptrace"; }

cleanup() { kubectl delete ns "$SMOKE_NS" >/dev/null 2>&1 || true; }
trap cleanup EXIT

main() {
  local plat; plat="$(platform)"
  info smoke action=begin "component=gvisor" "platform=${plat}"

  cluster_exists "$CLUSTER_NAME" || die 4 smoke no_cluster "集群 ${CLUSTER_NAME} 不存在，先 make up"

  # 1. 向每个 kind 节点容器落地 runsc + containerd 配置（best-effort）。
  local node failed=0
  for node in $(kind get nodes --name "$CLUSTER_NAME" 2>/dev/null); do
    info smoke action=install_runsc "component=${node}" "platform=${plat}"
    docker exec "$node" bash -c '
      set -e
      ARCH=$(uname -m); [ "$ARCH" = "x86_64" ] && ARCH=x86_64
      URL=https://storage.googleapis.com/gvisor/releases/release/latest/${ARCH}
      curl -fsSL ${URL}/runsc -o /usr/local/bin/runsc
      curl -fsSL ${URL}/containerd-shim-runsc-v1 -o /usr/local/bin/containerd-shim-runsc-v1
      chmod +x /usr/local/bin/runsc /usr/local/bin/containerd-shim-runsc-v1
      # containerd 注册 runsc runtime
      mkdir -p /etc/containerd
      grep -q runsc /etc/containerd/config.toml 2>/dev/null || cat >>/etc/containerd/config.toml <<TOML
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
TOML
      systemctl restart containerd 2>/dev/null || kill -SIGHUP $(pidof containerd) 2>/dev/null || true
    ' || failed=1
  done
  [ "$failed" -eq 0 ] || die 1 smoke runsc_install_fail "runsc 节点落地失败（可能网络/平台限制）；按 §4.6 降级链处置"

  # 2. 创建 RuntimeClass。
  kubectl apply -f - >/dev/null <<EOF
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: ${RUNTIME_CLASS}
handler: runsc
EOF

  # 3. 跑冒烟 Pod（runtimeClassName=gvisor，正常退出即通过）。
  kubectl create ns "$SMOKE_NS" >/dev/null 2>&1 || true
  kubectl -n "$SMOKE_NS" run runsc-smoke --restart=Never --image=registry.k8s.io/pause:3.9 \
    --overrides="{\"spec\":{\"runtimeClassName\":\"${RUNTIME_CLASS}\"}}" >/dev/null
  if kubectl -n "$SMOKE_NS" wait --for=condition=Ready pod/runsc-smoke --timeout=120s >/dev/null 2>&1; then
    info smoke result=pass "platform=${plat}"
    echo "runsc 冒烟通过（平台=${plat}）。"
    exit 0
  fi
  die 1 smoke pod_not_ready "runsc Pod 未 Ready；诊断: kubectl -n ${SMOKE_NS} describe pod runsc-smoke"
}
main "$@"
