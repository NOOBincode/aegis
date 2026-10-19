package main

import (
	"context"
	"testing"
	"time"

	k8sadapter "github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/mcpserver"
)

type fakeEvents struct{ events []k8sadapter.ObsEvent }

func (f *fakeEvents) ListEvents(context.Context, []string, time.Time, int) ([]k8sadapter.ObsEvent, error) {
	return f.events, nil
}

func TestEventTimelineFiltersAndTruncates(t *testing.T) {
	now := time.Now()
	var evs []k8sadapter.ObsEvent
	for i := 0; i < 3; i++ {
		evs = append(evs, k8sadapter.ObsEvent{
			Timestamp: now.Add(time.Duration(i) * time.Second), Type: "Warning", Reason: "BackOff",
			InvolvedKind: "Pod", InvolvedName: "p", InvolvedNS: "ns",
			SourceComponent: "kubelet", Message: "message", Count: 1,
		})
	}
	tool := eventTimelineTool(&fakeEvents{events: evs})
	got := tool.Handler(context.Background(), map[string]any{
		"namespaces": []any{"ns"}, "since": "2026-01-01T00:00:00Z", "limit": 2, "type_filter": "Warning", "involved_kind": "Pod",
	})
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	data, ok := got.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", got.Data)
	}
	events, ok := data["events"].([]map[string]any)
	if !ok {
		t.Fatalf("events type=%T", data["events"])
	}
	if data["truncated"] != true || len(events) != 2 {
		t.Fatalf("data=%+v", data)
	}
}

func TestEventTimelineBadSince(t *testing.T) {
	tool := eventTimelineTool(&fakeEvents{})
	got := tool.Handler(context.Background(), map[string]any{"namespaces": []any{"ns"}, "since": "not-a-time"})
	if got.Err == nil || got.Err.Class != mcpserver.ErrClassSchema {
		t.Fatalf("err=%+v", got.Err)
	}
}

func TestEventRegistration(t *testing.T) {
	reg := mcpserver.NewRegistry()
	reg.MustRegister(eventTimelineTool(&fakeEvents{}))
	if reg.Len() != 1 {
		t.Fatalf("tools=%d", reg.Len())
	}
}
