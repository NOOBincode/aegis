#!/usr/bin/env bash
# scripts/gen-mtls.sh —— 为 gatekeeper 审批端点生成本地 mTLS CA/server/client 证书。
# 只写 .cache/mtls 或指定目录（默认被 gitignore）；不打印私钥；生产请用 cert-manager/Vault PKI。
# 用法：bash scripts/gen-mtls.sh [out-dir]；AEGIS_MTLS_CLIENT=alice 控制客户端 CN。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="gen-mtls"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

OUT="${1:-${AEGIS_ROOT}/.cache/mtls}"
CLIENT_CN="${AEGIS_MTLS_CLIENT:-alice}"
mkdir -p "$OUT"
chmod 700 "$OUT"
umask 077

if [ -e "$OUT/ca.crt" ] && [ "${AEGIS_MTLS_FORCE:-0}" != "1" ]; then
  die 2 preflight exists "证书已存在：$OUT；设置 AEGIS_MTLS_FORCE=1 覆盖"
fi

openssl genrsa -out "$OUT/ca.key" 2048 >/dev/null 2>&1
openssl req -x509 -new -nodes -key "$OUT/ca.key" -sha256 -days 365 \
  -subj "/CN=aegis-local-ca" -out "$OUT/ca.crt" >/dev/null 2>&1

openssl genrsa -out "$OUT/server.key" 2048 >/dev/null 2>&1
openssl req -new -key "$OUT/server.key" -subj "/CN=aegis-gatekeeper" -out "$OUT/server.csr" >/dev/null 2>&1
cat > "$OUT/server.ext" <<EXT
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,DNS:aegis-gatekeeper,DNS:gatekeeper,IP:127.0.0.1
EXT
openssl x509 -req -in "$OUT/server.csr" -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" -CAcreateserial \
  -days 365 -sha256 -extfile "$OUT/server.ext" -out "$OUT/server.crt" >/dev/null 2>&1

openssl genrsa -out "$OUT/client.key" 2048 >/dev/null 2>&1
openssl req -new -key "$OUT/client.key" -subj "/CN=${CLIENT_CN}" -out "$OUT/client.csr" >/dev/null 2>&1
cat > "$OUT/client.ext" <<EXT
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
EXT
openssl x509 -req -in "$OUT/client.csr" -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" -CAcreateserial \
  -days 365 -sha256 -extfile "$OUT/client.ext" -out "$OUT/client.crt" >/dev/null 2>&1
chmod 600 "$OUT"/*.key
rm -f "$OUT"/*.csr "$OUT"/*.ext

info certs "dir=$OUT" "client_cn=$CLIENT_CN" "ca=$OUT/ca.crt" "server=$OUT/server.crt" "client=$OUT/client.crt"
