#!/usr/bin/env bash
# scripts/install-tools.sh —— 安装/校验 kind、kubectl、helm（钉死版本，幂等）。
# aegis-ci 红线 B-4：本地 WSL2 与 CI 共用这一份安装脚本，禁止在 workflow 里写第二份安装逻辑。
# 退出码：0 全部就绪；2 安装失败。
# 用法：install-tools.sh            —— 缺什么装什么（已装且版本符则跳过）
#       INSTALL_DIR=/usr/local/bin install-tools.sh   （CI 用；默认 ~/.local/bin 免 sudo）
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="install-tools"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$INSTALL_DIR"
OS=linux; ARCH=amd64

# have <bin> <want_version>：已安装且版本一致返回 0。
have() {
  local bin="$1" want="$2"
  command -v "$bin" >/dev/null 2>&1 || return 1
  local got; got="$("$bin" version 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
  [ "$got" = "$want" ]
}

install_kind() {
  local v="${AEGIS_KIND_VERSION:?}"
  have kind "$v" && { info install component=kind result=skip "version=${v}"; return 0; }
  info install component=kind action=download "version=${v}"
  curl -fsSL "https://kind.sigs.k8s.io/dl/${v}/kind-${OS}-${ARCH}" -o "${INSTALL_DIR}/kind" \
    || die 2 install kind_download_fail "kind ${v} 下载失败（网络/镜像源）"
  chmod +x "${INSTALL_DIR}/kind"
}

install_kubectl() {
  local v="${AEGIS_KUBECTL_VERSION:?}"
  have kubectl "$v" && { info install component=kubectl result=skip "version=${v}"; return 0; }
  info install component=kubectl action=download "version=${v}"
  curl -fsSL "https://dl.k8s.io/release/${v}/bin/${OS}/${ARCH}/kubectl" -o "${INSTALL_DIR}/kubectl" \
    || die 2 install kubectl_download_fail "kubectl ${v} 下载失败（网络/镜像源）"
  chmod +x "${INSTALL_DIR}/kubectl"
}

install_helm() {
  local v="${AEGIS_HELM_VERSION:?}"
  have helm "$v" && { info install component=helm result=skip "version=${v}"; return 0; }
  info install component=helm action=download "version=${v}"
  local tmp; tmp="$(mktemp -d)"
  curl -fsSL "https://get.helm.sh/helm-${v}-${OS}-${ARCH}.tar.gz" | tar -xz -C "$tmp" \
    || die 2 install helm_download_fail "helm ${v} 下载失败（网络/镜像源）"
  mv "${tmp}/${OS}-${ARCH}/helm" "${INSTALL_DIR}/helm"; chmod +x "${INSTALL_DIR}/helm"; rm -rf "$tmp"
}

main() {
  info install action=begin "install_dir=${INSTALL_DIR}"
  install_kind; install_kubectl; install_helm
  command -v docker >/dev/null 2>&1 \
    || warn install "error_class=docker_missing" "hint=docker 需单独装（kind 后端）；本脚本不装 docker"
  info install action=done result=ok
  echo "工具就绪于 ${INSTALL_DIR}（确保在 PATH）。kind=${AEGIS_KIND_VERSION} kubectl=${AEGIS_KUBECTL_VERSION} helm=${AEGIS_HELM_VERSION}"
}
main "$@"
