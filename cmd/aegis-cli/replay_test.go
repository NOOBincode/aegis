package main

import (
	"testing"
	"time"

	"github.com/aegis-dev/aegis/internal/audit"
)

func TestRecordConversionRoundTrip(t *testing.T) {
	rec := audit.Record{
		SessionRef: "s1,s2",
		Outcome:    "OOM_KILLED",
		ToolCalls: []audit.ToolCall{{
			Seq: 1, Timestamp: time.Date(2026, 10, 18, 17, 1, 10, 0, time.UTC),
			SessionID: "s1", Tool: "scan_cluster", ArgsPreview: `{"namespace":"aegis-eval"}`,
			ResultSHA256: "abc", ResultBytes: 42, RedactedCount: 2, LatencyMs: 5,
		}},
	}
	dr := apiRecordFromAudit(rec)
	if dr.Name == "" || dr.Spec.SessionRef != rec.SessionRef || len(dr.Spec.ToolCalls) != 1 {
		t.Fatalf("api record=%+v", dr)
	}
	back := auditRecordFromAPI(&dr)
	if back.SessionRef != rec.SessionRef || back.ToolCalls[0].Tool != "scan_cluster" || back.ToolCalls[0].RedactedCount != 2 {
		t.Fatalf("roundtrip=%+v", back)
	}
}
