package prom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") == "" {
			t.Fatalf("unexpected path/query: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"p1"},"value":[1710000000.5,"42"]}]}}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, time.Second, 50)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Query(context.Background(), `up`, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultType != "vector" || len(res.Series) != 1 || len(res.Series[0].Values) != 1 || res.Series[0].Values[0].Value != 42 {
		t.Fatalf("result=%+v", res)
	}
}

func TestQueryRangeMatrixAndError(t *testing.T) {
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error"}`))
			return
		}
		if r.URL.Path != "/api/v1/query_range" || r.URL.Query().Get("step") == "" {
			t.Fatalf("unexpected: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1710000000,"1"],[1710000015,"2"]]}]}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, time.Second, 50)
	res, err := c.QueryRange(context.Background(), `rate(up[1m])`, time.Unix(1710000000, 0), time.Unix(1710000100, 0), 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 1 || len(res.Series[0].Values) != 2 {
		t.Fatalf("res=%+v", res)
	}
	fail = true
	if _, err := c.Query(context.Background(), `up`, time.Time{}); err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("应透传 Prometheus 错误，got %v", err)
	}
}

func TestValidation(t *testing.T) {
	if _, err := New("not-a-url", time.Second, 1); err == nil {
		t.Fatal("非法 URL 应报错")
	}
	c, _ := New("http://localhost:9090", time.Second, 1)
	if _, err := c.QueryRange(context.Background(), "up", time.Unix(10, 0), time.Unix(10, 0), time.Second); err == nil {
		t.Fatal("end<=start 应报错")
	}
}
