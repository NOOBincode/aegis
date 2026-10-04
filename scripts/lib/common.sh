# scripts/lib/common.sh —— aegis 自动化脚本共享库（scripts/README.md §4.1 骨架四要素）。
# 只被 source，不直接执行。提供：结构化日志、前置检查、版本比对、超时等待、失败诊断。
# 纪律：脚本只搭环境，放行判断永不进脚本（scripts/README.md §7）；不携带真实凭据。

# 由 source 方设定 SCRIPT_NAME；缺省取调用脚本名。
SCRIPT_NAME="${SCRIPT_NAME:-$(basename "${BASH_SOURCE[1]:-common.sh}" .sh)}"

# 版本钉死来源（单一事实源的机器可读形式）。
AEGIS_ROOT="${AEGIS_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
# shellcheck source=../../deploy/versions.env
[ -f "${AEGIS_ROOT}/deploy/versions.env" ] && . "${AEGIS_ROOT}/deploy/versions.env"

# ---- 结构化日志（scripts/README.md §6 字段口径）----
# 字段：ts level script_name step component action result duration_ms error_class hint
log() { # log <level> <step> <result> [key=value ...]
  local level="$1" step="$2" result="$3"; shift 3
  printf '{"ts":"%s","level":"%s","script_name":"%s","step":"%s","result":"%s"%s}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$level" "$SCRIPT_NAME" "$step" "$result" \
    "$(for kv in "$@"; do printf ',"%s":"%s"' "${kv%%=*}" "${kv#*=}"; done)"
}
info()  { log info  "$1" ok       "${@:2}"; }
warn()  { log warn  "$1" degraded "${@:2}"; }
fail()  { log error "$1" fail     "${@:2}"; }

# die <exit_code> <step> <error_class> <hint>：输出诊断后按退出码语义退出（§4.2）。
die() {
  local code="$1" step="$2" class="$3" hint="$4"
  log error "$step" fail "error_class=${class}" "hint=${hint}"
  exit "$code"
}

# ---- 前置检查（§5：前置缺失 → 退出码 2，fail-fast）----
require_bin() { # require_bin <bin> <expected_version> <install_hint>
  local bin="$1" want="$2" hint="$3"
  command -v "$bin" >/dev/null 2>&1 \
    || die 2 preflight missing_bin "${bin} 未安装。${hint}"
  if [ -n "$want" ]; then
    local got; got="$("$bin" version 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
    [ -n "$got" ] || got="unknown"
    log info preflight ok "component=${bin}" "version=${got}" "action=version_check"
  fi
}

require_docker() {
  docker info >/dev/null 2>&1 \
    || die 2 preflight docker_down "docker 守护进程不可用。WSL2: sudo systemctl enable --now docker；或 Docker Desktop 勾选 WSL2 集成"
}

# ---- 超时等待（§5：组件不就绪 → 退出码 3）----
# wait_deploy <namespace> <deployment> <timeout_seconds>
wait_deploy() {
  local ns="$1" name="$2" timeout="${3:-300}"
  info "wait_ready" "component=${name}" "action=rollout_wait" "timeout_s=${timeout}"
  kubectl -n "$ns" rollout status "deployment/${name}" --timeout="${timeout}s" >/dev/null 2>&1 \
    || die 3 wait_ready timeout "${ns}/${name} ${timeout}s 内未 Ready。诊断: kubectl -n ${ns} get pods; kubectl -n ${ns} describe deploy ${name}"
  info "wait_ready" "component=${name}" "result=ready"
}

# ---- 失败诊断陷阱（§4.1 要素 4）----
# 用法：在脚本 set -euo pipefail 之后 `trap_common_err`。
trap_common_err() {
  trap 'rc=$?; [ $rc -ne 0 ] && fail "trap" "error_class=errexit" "hint=上一命令失败(rc=${rc})，行号见上；可用 bash -x 复跑定位" >&2' ERR
}

# ---- 幂等辅助 ----
# 集群存在性（env-up 幂等检查 / env-down 现状检测）。
cluster_exists() { kind get clusters 2>/dev/null | grep -qx "${1:-aegis}"; }
