#!/usr/bin/env bash
# scripts/check-secrets.sh —— 仓库/历史密钥泄漏检查（只输出路径/行号，不回显命中内容）。
# 覆盖：
#   1. 当前 tracked + untracked（非 ignore）文件中的 API key/私钥/平台 token 模式
#   2. git 历史提交中的同一组高置信模式（只打印 commit OID）
# 排除：.agents/skills（第三方 skill 文档中的公开示例）、.git、.cache
# 用法：check-secrets.sh [--history]
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="check-secrets"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

cd "$AEGIS_ROOT"
HISTORY=0
[ "${1:-}" = "--history" ] && HISTORY=1

if ! python3 - <<'PYEOF'
import os, re, subprocess, sys

# 高置信模式；不覆盖所有秘密形态，只保证"明显 key/私钥"不会静默入仓。
patterns = [
    ("openai-deepseek-key", re.compile(r"sk-[A-Za-z0-9_-]{20,}")),
    ("aws-access-key", re.compile(r"AKIA[0-9A-Z]{16}")),
    ("github-token", re.compile(r"gh[pousr]_[A-Za-z0-9]{30,}")),
    ("gitlab-token", re.compile(r"glpat-[A-Za-z0-9_-]{20,}")),
    ("slack-token", re.compile(r"xox[baprs]-[A-Za-z0-9-]{10,}")),
    ("pem-private-key", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")),
]

files = subprocess.check_output(["git", "ls-files", "-co", "--exclude-standard"], text=True).splitlines()
hits = []
for f in files:
    norm = f.replace("\\", "/")
    if norm.startswith(".agents/skills/"):
        continue
    try:
        if os.path.getsize(f) > 2_000_000:
            continue
        data = open(f, "r", encoding="utf-8", errors="ignore").read()
    except OSError:
        continue
    for name, rx in patterns:
        for m in rx.finditer(data):
            line = data.count("\n", 0, m.start()) + 1
            hits.append((f, line, name))
            break  # 同一文件同一规则只报一次，避免刷屏
if hits:
    for f, line, name in hits:
        print(f"LEAK {f}:{line} pattern={name}")
    sys.exit(1)
print(f"secrets: clean ({len(files)} files scanned)")
PYEOF
then
  log error current fail "hint=当前工作树存在高置信密钥模式；先移除并轮换"
  exit 1
fi
log info current pass

if [ "$HISTORY" -eq 1 ]; then
  # git log -G 只打印 commit OID，避免把历史中的密钥再次输出到终端/CI 日志。
  if git log --all -G 'sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----' --format=%H >/tmp/aegis-secret-history.$$ 2>/dev/null; then
    if [ -s /tmp/aegis-secret-history.$$ ]; then
      cat /tmp/aegis-secret-history.$$
      rm -f /tmp/aegis-secret-history.$$
      log error history fail "hint=git 历史命中高置信密钥模式；需 rewrite/清理后再推送"
      exit 1
    fi
  fi
  rm -f /tmp/aegis-secret-history.$$
  log info history pass
fi
log info summary pass
