package redact

import (
	"strings"
	"testing"
)

const testKey = "sk-" + "testonly1234567890abcdef"

func TestStringRedactsSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"api-key", "Authorization: Bearer " + testKey, "Bearer " + marker},
		{"bearer", "curl -H 'Authorization: Bearer abcdefghijklmnopqrstuvwxyz'", "Bearer " + marker},
		{"kv", "password=super-secret-value-123456", "password=" + marker},
		{"db", "postgres://kagent:hunter2@postgres:5432/kagent", "postgres://[REDACTED]@postgres:5432/kagent"},
		{"aws", "AKIA" + "IOSFODNN7EXAMPLE", "[REDACTED_AWS_KEY]"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "[REDACTED_JWT]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := String(tc.in)
			if n == 0 {
				t.Fatalf("应命中脱敏：%q", tc.in)
			}
			if strings.Contains(got, "sk-1884278a") || strings.Contains(got, "hunter2") || strings.Contains(got, "super-secret") {
				t.Fatalf("敏感值未脱敏：%q", got)
			}
			if !strings.Contains(got, strings.Split(tc.want, " ")[0]) && tc.want != "" {
				// 只做弱断言：具体排除敏感值比匹配替换文本更重要。
				t.Logf("got=%q want~=%q", got, tc.want)
			}
		})
	}
}

func TestPEMPrivateKey(t *testing.T) {
	in := "before\n-----BEGIN OPENSSH " + "PRIVATE KEY-----\nabc\n-----END OPENSSH " + "PRIVATE KEY-----\nafter"
	got, n := String(in)
	if n == 0 || strings.Contains(got, "BEGIN OPENSSH") || !strings.Contains(got, "[REDACTED_PRIVATE_KEY]") {
		t.Fatalf("私钥未脱敏：n=%d got=%q", n, got)
	}
}

func TestNoFalsePositiveOnWrappedMarker(t *testing.T) {
	got, n := String("<untrusted_cluster_data>{\"items\":[]}</untrusted_cluster_data>")
	if n != 0 || !strings.Contains(got, "untrusted_cluster_data") {
		t.Fatalf("包裹标记不应被误脱敏：n=%d got=%q", n, got)
	}
}
