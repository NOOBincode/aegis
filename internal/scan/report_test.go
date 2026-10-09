package scan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleResult() Result {
	return Result{
		GeneratedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
		ScannedPods: 3,
		Findings: []Finding{
			{
				ID: "OOM_KILLED/Pod/shop/payments-0", RuleID: "OOM_KILLED",
				Severity: SeverityCritical, Confidence: ConfidenceHigh,
				Object:    ObjRef{Kind: "Pod", Namespace: "shop", Name: "payments-0"},
				RootCause: "容器 web 因内存超限被 OOMKilled",
			},
			{
				ID: "policy/reliability.no-memory-limit/Deployment/shop/api", RuleID: "policy/reliability.no-memory-limit",
				Severity: SeverityWarning, Confidence: ConfidenceHigh,
				Object:    ObjRef{Kind: "Deployment", Namespace: "shop", Name: "api"},
				RootCause: "容器未设 memory limit",
			},
		},
		ResolvedByRules: 2,
	}
}

// TestWriteJSON 验证版本化 JSON 报告的 schema/summary/严重度统计。
func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleResult()); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("非法 JSON: %v", err)
	}
	if doc["schema"] != schemaScanV1 {
		t.Errorf("schema=%v, 期望 %q", doc["schema"], schemaScanV1)
	}
	sum, ok := doc["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary 类型错误")
	}
	crit, _ := sum["critical"].(float64)
	warn, _ := sum["warning"].(float64)
	if crit != 1 || warn != 1 {
		t.Errorf("严重度统计错误：%v", sum)
	}
	if execOK, _ := doc["execution_successful"].(bool); !execOK {
		t.Errorf("无盲点应 execution_successful=true")
	}
}

// TestWriteSARIF 验证 SARIF 结构：severity 映射、k8s:// URI、只声明触发 rule、盲点语义。
func TestWriteSARIF(t *testing.T) {
	var buf bytes.Buffer
	r := sampleResult()
	if err := WriteSARIF(&buf, r); err != nil {
		t.Fatalf("WriteSARIF: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"version": "2.1.0"`) {
		t.Errorf("缺 SARIF 版本")
	}
	if !strings.Contains(out, `"level": "error"`) || !strings.Contains(out, `"level": "warning"`) {
		t.Errorf("severity 映射错误（critical→error, warning→warning）：%s", out)
	}
	if !strings.Contains(out, "k8s://shop/Pod/payments-0") {
		t.Errorf("缺 k8s:// URI：%s", out)
	}
	if !strings.Contains(out, `"executionSuccessful": true`) {
		t.Errorf("无盲点应 executionSuccessful=true")
	}

	// 有盲点时 executionSuccessful=false（不产出"看着绿其实瞎"的干净结果）。
	r.PartialReads = []string{"secrets:Forbidden"}
	buf.Reset()
	if err := WriteSARIF(&buf, r); err != nil {
		t.Fatalf("WriteSARIF: %v", err)
	}
	if !strings.Contains(buf.String(), `"executionSuccessful": false`) {
		t.Errorf("有盲点应 executionSuccessful=false")
	}
}

// TestReportNoClusterIdentity 脱敏检查：报告不得含集群身份/敏感字段。
func TestReportNoClusterIdentity(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleResult()); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	out := buf.String()
	for _, sensitive := range []string{"apiserver", "kubeconfig", "secret", "password", "10.0.0.", "Bearer"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(sensitive)) {
			t.Errorf("报告含敏感字段 %q", sensitive)
		}
	}
}
