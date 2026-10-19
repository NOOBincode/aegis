package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aegis-dev/aegis/internal/adapters/prom"
	"github.com/aegis-dev/aegis/internal/mcpserver"
)

type fakeQuerier struct {
	res *prom.Result
	err error
}

func (f *fakeQuerier) Query(context.Context, string, time.Time) (*prom.Result, error) {
	return f.res, f.err
}
func (f *fakeQuerier) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (*prom.Result, error) {
	return f.res, f.err
}

func TestPromQueryToolTruncates(t *testing.T) {
	res := &prom.Result{ResultType: "vector"}
	for i := 0; i < 60; i++ {
		res.Series = append(res.Series, prom.Series{Metric: map[string]string{"pod": fmt.Sprintf("p%02d", i)}, Values: []prom.Point{{Timestamp: time.Unix(100, 0), Value: float64(i)}}})
	}
	tool := promQueryTool(&fakeQuerier{res: res})
	got := tool.Handler(context.Background(), map[string]any{"query": "up"})
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	data, ok := got.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", got.Data)
	}
	series, ok := data["series"].([]map[string]any)
	if !ok {
		t.Fatalf("series type=%T", data["series"])
	}
	if data["truncated"] != true || len(series) != 50 {
		t.Fatalf("data=%+v", data)
	}
}

func TestPromRangeRejectsSpanAndRecomputesStep(t *testing.T) {
	f := &fakeQuerier{res: &prom.Result{ResultType: "matrix"}}
	tool := promRangeTool(f)
	start := "2026-01-01T00:00:00Z"
	end := "2026-01-01T02:00:00Z"
	if got := tool.Handler(context.Background(), map[string]any{"query": "up", "start": start, "end": end, "step_seconds": 15}); got.Err == nil {
		t.Fatal(">1h 应拒绝")
	}
	// 1h + 5s step 应被放大到 >=15s，且不报错。
	end = "2026-01-01T01:00:00Z"
	got := tool.Handler(context.Background(), map[string]any{"query": "up", "start": start, "end": end, "step_seconds": 15})
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	data, ok := got.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", got.Data)
	}
	pr, ok := data["params_resolved"].(map[string]any)
	if !ok {
		t.Fatalf("params type=%T", data["params_resolved"])
	}
	step, ok := pr["step_seconds"].(int)
	if !ok || step < 15 {
		t.Fatalf("step=%v", pr)
	}
}

func TestPromRegistration(t *testing.T) {
	reg := mcpserver.NewRegistry()
	reg.MustRegister(promQueryTool(&fakeQuerier{}))
	reg.MustRegister(promRangeTool(&fakeQuerier{}))
	if reg.Len() != 2 {
		t.Fatalf("tools=%d", reg.Len())
	}
}

func TestPromUpstreamErrorClass(t *testing.T) {
	tool := promQueryTool(&fakeQuerier{err: fmt.Errorf("boom")})
	got := tool.Handler(context.Background(), map[string]any{"query": "up"})
	if got.Err == nil || got.Err.Class != mcpserver.ErrClassDownstream {
		t.Fatalf("err=%+v", got.Err)
	}
}
