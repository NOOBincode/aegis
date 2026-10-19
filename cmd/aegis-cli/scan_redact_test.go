package main

import (
	"strings"
	"testing"

	"github.com/aegis-dev/aegis/internal/scan"
)

// CLI 报告与 MCP 出口同一脱敏口径：finding 证据/建议/盲点不得裸落 token。
func TestRedactScanResult(t *testing.T) {
	res := scan.Result{
		Findings: []scan.Finding{{
			RootCause:  "auth failed with Bearer " + "sk-" + "testonly1234567890abcdef",
			Evidence:   []string{"password=hunter2hunter2", "normal evidence"},
			Suggestion: "rotate key " + "sk-" + "testonly1234567890abcdef",
		}},
		PartialReads: []string{"postgres://user:pass@db:5432/x"},
	}
	redactScanResult(&res)
	all := res.Findings[0].RootCause + strings.Join(res.Findings[0].Evidence, "|") + res.Findings[0].Suggestion + strings.Join(res.PartialReads, "|")
	if strings.Contains(all, "sk-1884278a") || strings.Contains(all, "hunter2") || strings.Contains(all, "user:pass@") {
		t.Fatalf("报告未脱敏：%q", all)
	}
	if !strings.Contains(all, "[REDACTED]") {
		t.Fatalf("应含脱敏标记：%q", all)
	}
}
