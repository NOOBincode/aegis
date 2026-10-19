package audit

import (
	"bytes"
	"strings"
	"testing"
)

const sampleLog = `{"time":"2026-10-18T17:01:10.939Z","level":"INFO","msg":"mcp.tool.invoke","tool_name":"scan_cluster","risk_hint":"R0","latency_ms":5,"error_class":"","session_id":"s1","args_preview":"{\"namespace\":\"aegis-eval\"}","result_sha256":"abc123","result_bytes":42,"result_preview":"findings: OOM_KILLED","redacted_count":0}
not json
{"time":"2026-10-18T17:01:14.967Z","level":"INFO","msg":"mcp.tool.invoke","tool_name":"get_pod_logs","session_id":"s2","args_preview":"{\"name\":\"x\"}","result_sha256":"def456","result_bytes":128,"result_preview":"line1","redacted_count":2,"error_class":""}
{"msg":"other"}
`

func TestParseLog(t *testing.T) {
	calls, err := ParseLog(strings.NewReader(sampleLog))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%d", len(calls))
	}
	if calls[0].Tool != "scan_cluster" || calls[0].Seq != 1 {
		t.Fatalf("first=%+v", calls[0])
	}
	if calls[1].Tool != "get_pod_logs" || calls[1].RedactedCount != 2 {
		t.Fatalf("second=%+v", calls[1])
	}
	rec := BuildRecord(calls)
	if !strings.Contains(rec.SessionRef, "s1") || !strings.Contains(rec.SessionRef, "s2") {
		t.Fatalf("sessionRef=%q", rec.SessionRef)
	}
	if rec.ClusterSnapshotRef != "sha256:abc123" {
		t.Fatalf("clusterSnapshotRef=%q", rec.ClusterSnapshotRef)
	}
	if len(rec.SanitizationEvents) != 1 {
		t.Fatalf("sanitize events=%d", len(rec.SanitizationEvents))
	}
	var out bytes.Buffer
	if err := Render(&out, rec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cluster_snapshot_ref=sha256:abc123") || !strings.Contains(out.String(), "scan_cluster") || !strings.Contains(out.String(), "get_pod_logs") {
		t.Fatalf("render=%s", out.String())
	}
}
