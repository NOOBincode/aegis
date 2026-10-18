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
  local code="$1" step="$2" class="$3" hint="${4:-}"
  log error "$step" fail "error_class=${class}" "hint=${hint}"
  exit "$code"
}

# ---- 前置检查（§5：前置缺失 → 退出码 2，fail-fast）----
# normalize_version 去掉前导 v 与 helm 的 +g... 后缀，供版本严格比对。
normalize_version() {
  local v="$1"
  v="${v#v}"
  v="${v%%+*}"
  printf '%s' "$v"
}

require_bin() { # require_bin <bin> <expected_version> <install_hint>
  local bin="$1" want="$2" hint="$3"
  command -v "$bin" >/dev/null 2>&1 \
    || die 2 preflight missing_bin "${bin} 未安装。${hint}"
  if [ -n "$want" ]; then
    local got; got="$("$bin" version 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
    [ -n "$got" ] || got="unknown"
    # 版本漂移必须 fail-closed（deploy/README.md §5：实装版本与版本表不一致 → 拒绝继续）。
    if [ "$(normalize_version "$got")" != "$(normalize_version "$want")" ]; then
      die 2 preflight version_mismatch "${bin} 版本漂移：want=${want} got=${got}。${hint}；先 bash scripts/install-tools.sh"
    fi
    log info preflight ok "component=${bin}" "version=${got}" "action=version_check"
  fi
}

# require_host_inotify：WSL2/容器宿主 inotify 实例耗尽会让 kube-proxy/CoreDNS 报
# "too many open files" 并拖垮整个集群（环境级故障，不是代码可自愈的）。低于门槛
# 一律拒绝继续，并打印可直接执行的 sysctl 修复命令（I10：失败必须响）。
require_host_inotify() {
  local min_i="${AEGIS_HOST_INOTIFY_MIN_INSTANCES:-512}"
  local min_w="${AEGIS_HOST_INOTIFY_MIN_WATCHES:-262144}"
  local got_i got_w
  got_i="$(sysctl -n fs.inotify.max_user_instances 2>/dev/null || echo unknown)"
  got_w="$(sysctl -n fs.inotify.max_user_watches 2>/dev/null || echo unknown)"
  if [ "$got_i" != "unknown" ] && [ "$got_i" -lt "$min_i" ] 2>/dev/null; then
    die 2 preflight host_inotify_limit "fs.inotify.max_user_instances=${got_i} < ${min_i}。先在宿主执行: printf 'fs.inotify.max_user_instances = ${min_i}\\nfs.inotify.max_user_watches = ${min_w}\\n' | sudo tee /etc/sysctl.d/99-aegis.conf && sudo sysctl --system；然后重跑 make up"
  fi
  if [ "$got_w" != "unknown" ] && [ "$got_w" -lt "$min_w" ] 2>/dev/null; then
    die 2 preflight host_inotify_limit "fs.inotify.max_user_watches=${got_w} < ${min_w}。同见 /etc/sysctl.d/99-aegis.conf 修复"
  fi
  log info preflight ok "component=host_inotify" "max_user_instances=${got_i}" "max_user_watches=${got_w}"
}

# fetch_chart <url> <sha256> <name>：下载并校验 chart 包到工作区缓存，结果写入
# AEGIS_FETCHED_CHART（不污染 stdout：调用方在 main 流程里直接读该变量，避免把日志
# 行混进 helm 的 chart 路径）。chart 一律带 SHA256 钉死（供应链纪律）。
AEGIS_FETCHED_CHART=""
fetch_chart() {
  local url="$1" sha="$2" name="$3"
  local dir="${AEGIS_CHART_CACHE:-${AEGIS_ROOT}/.cache/charts}"
  mkdir -p "$dir"
  local out="${dir}/${name}.tgz"
  if [ -f "$out" ] && [ "$(sha256sum "$out" | awk '{print $1}')" = "$sha" ]; then
    AEGIS_FETCHED_CHART="$out"; return 0
  fi
  info preflight "component=${name}" action=chart_download "url=${url}"
  curl -fsSL --retry 2 --connect-timeout 10 -o "${out}.tmp" "$url" \
    || die 2 preflight chart_download_fail "${name} chart 下载失败：${url}"
  local got; got="$(sha256sum "${out}.tmp" | awk '{print $1}')"
  if [ "$got" != "$sha" ]; then
    rm -f "${out}.tmp"
    die 2 preflight chart_checksum_mismatch "${name} chart SHA256 不符：want=${sha} got=${got}"
  fi
  mv "${out}.tmp" "$out"
  AEGIS_FETCHED_CHART="$out"
}

# namespace_not_ready_pods <ns>：打印未就绪 Pod（Ready 列不是 n/n，排除成功完成的 Job）。
namespace_not_ready_pods() {
  local ns="$1"
  kubectl -n "$ns" get pods --no-headers 2>/dev/null | awk '{
    split($2, a, "/");
    if (a[1] != a[2] && $3 != "Completed" && $3 != "Succeeded") print $1 "(" $2 "," $3 ")"
  }'
}

# require_namespace_ready <ns> <component>：命名空间内任一 Pod 未就绪即 fail（I10）。
require_namespace_ready() {
  local ns="$1" component="$2"
  local bad; bad="$(namespace_not_ready_pods "$ns")"
  if [ -n "$bad" ]; then
    echo "未就绪 Pod：${bad}" >&2
    kubectl -n "$ns" get pods 2>/dev/null >&2 || true
    die 1 health "${component}_unready" "${ns} 存在未就绪 Pod；先查 kubectl -n ${ns} get pods / describe"
  fi
  info health "component=${component}" result=ready
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
