// Package redact 是纯 stdlib 的敏感信息脱敏器（M1 R24 的库形态）。
//
// 用途：在集群数据进入 LLM/审计日志之前，把 API key、Bearer token、私钥、
// 连接串凭据、JWT、AWS access key 等替换成 [REDACTED_*] 标记。它只做模式化
// 最小替换，不追求覆盖所有秘密形态；模式库可评审、可扩展，且与 I4 包裹解耦：
// 先脱敏、再包裹/截断，避免秘密进入外部模型或本地日志。
package redact

import (
	"regexp"
	"strings"
)

// marker 是统一替换标记；不保留任何原值片段（连前缀都不留）。
const marker = "[REDACTED]"

var rules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// PEM 私钥：多行匹配；先于其它规则，避免 base64 内容被部分替换后破坏结构。
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED_PRIVATE_KEY]"},
	// OpenAI/DeepSeek 等 sk- 前缀 key。
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`), "[REDACTED_API_KEY]"},
	// AWS access key id（secret key 没有固定前缀，靠下面的 key=value 规则）。
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED_AWS_KEY]"},
	// JWT（header.payload.signature，三段 base64url）。
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), "[REDACTED_JWT]"},
	// Authorization: Bearer xxx。
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`), "Bearer " + marker},
	// key/token/password/secret = value（保留键名，便于审计定位；值整体替换）。
	{regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?key|secret|token|password|passwd)\s*[:=]\s*["']?)([A-Za-z0-9._~+/=-]{12,})`), "${1}" + marker},
	// 数据库/缓存连接串中的 user:pass@ 段。
	{regexp.MustCompile(`(?i)\b(postgres|postgresql|mysql|mongodb|redis)://[^@\s]+@`), "${1}://[REDACTED]@"},
}

// String 返回脱敏后的字符串与替换次数。替换次数用于审计指标
// （aegis_redacted_total / DecisionRecord.sanitizationEvents），不记录原值。
func String(s string) (string, int) {
	total := 0
	for _, r := range rules {
		n := len(r.re.FindAllStringIndex(s, -1))
		if n == 0 {
			continue
		}
		s = r.re.ReplaceAllString(s, r.repl)
		total += n
	}
	return s, total
}

// Count 只统计命中次数，不返回脱敏文本（避免日志路径反复持有原文）。
func Count(s string) int {
	total := 0
	for _, r := range rules {
		total += len(r.re.FindAllStringIndex(s, -1))
	}
	return total
}

// HasSecret 是便捷判断（测试/告警用），不用于替换。
func HasSecret(s string) bool { return Count(s) > 0 }

// Clean 是 String 的忽略计数形态。
func Clean(s string) string {
	out, _ := String(s)
	return out
}

// IsAllPlaceholder 判断字符串是否只由占位符构成（防止测试把 [REDACTED] 当秘密）。
func IsAllPlaceholder(s string) bool {
	return strings.TrimSpace(s) == marker
}
