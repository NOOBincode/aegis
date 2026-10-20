package opa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEvaluateBoolAndObject(t *testing.T) {
	mode := "allow"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/data/aegis/allow" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"result":` + map[string]string{
			"allow": `true`, "deny": `false`, "obj": `{"allow":false,"reason":"quota"}`,
		}[mode] + `}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Evaluate(context.Background(), "aegis/allow", map[string]any{"x": 1})
	if err != nil || !d.Allowed {
		t.Fatalf("allow: %+v err=%v", d, err)
	}
	mode = "deny"
	d, err = c.Evaluate(context.Background(), "aegis/allow", nil)
	if err != nil || d.Allowed {
		t.Fatalf("deny: %+v err=%v", d, err)
	}
	mode = "obj"
	d, err = c.Evaluate(context.Background(), "aegis/allow", nil)
	if err != nil || d.Allowed || d.Reason != "quota" {
		t.Fatalf("obj: %+v err=%v", d, err)
	}
}

func TestEvaluateFailClosedOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "engine down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, time.Second)
	if _, err := c.Evaluate(context.Background(), "aegis/allow", nil); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("应返回错误而非放行：%v", err)
	}
}
