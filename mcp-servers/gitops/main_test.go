package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenChangePRTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/gitops/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/gitops/git/refs":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/gitops/contents/clusters/prod/app.yaml":
			http.NotFound(w, r)
		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/gitops/contents/clusters/prod/app.yaml":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["branch"] == "" || body["content"] == "" {
				t.Fatalf("bad file body=%v", body)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/gitops/pulls":
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/org/gitops/pull/1"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("AEGIS_GIT_BASE_URL", srv.URL)
	t.Setenv("AEGIS_GIT_TOKEN", "tok")
	t.Setenv("AEGIS_GIT_REPO_WHITELIST", "org/gitops")
	t.Setenv("AEGIS_GIT_BASE_BRANCH", "main")
	tool := openChangePRTool(slogDiscard())
	res := tool.Handler(context.Background(), map[string]any{
		"repo": "org/gitops", "cr_ref": "cr-1", "description": "scale payments",
		"manifest_patch": "kind: ConfigMap\nmetadata:\n  name: x\n",
		"diagnosis":      "OOM", "blast_radius": "1 pod", "risk_level": "R2", "rollback": "revert PR",
		"file_path": "clusters/prod/app.yaml",
	})
	if res.Err != nil {
		t.Fatalf("err=%+v", res.Err)
	}
	data, _ := res.Data.(map[string]any)
	if data["pr_url"] != "https://github.com/org/gitops/pull/1" || data["no_cluster_mutation"] != true {
		t.Fatalf("data=%v", res.Data)
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestOpenChangePRFailClosedWithoutWhitelist(t *testing.T) {
	t.Setenv("AEGIS_GIT_REPO_WHITELIST", "")
	t.Setenv("AEGIS_GIT_TOKEN", "tok")
	tool := openChangePRTool(slogDiscard())
	res := tool.Handler(context.Background(), map[string]any{
		"repo": "org/gitops", "cr_ref": "cr-1", "description": "x", "manifest_patch": "x",
		"diagnosis": "d", "blast_radius": "1", "risk_level": "R2", "rollback": "r",
		"file_path": "clusters/prod/x.yaml",
	})
	if res.Err == nil {
		t.Fatal("无白名单应 fail-closed")
	}
}

func TestOpenChangePRRejectsNonWhitelistedRepo(t *testing.T) {
	t.Setenv("AEGIS_GIT_REPO_WHITELIST", "org/allowed")
	t.Setenv("AEGIS_GIT_TOKEN", "tok")
	tool := openChangePRTool(slogDiscard())
	res := tool.Handler(context.Background(), map[string]any{
		"repo": "org/evil", "cr_ref": "cr-1", "description": "x", "manifest_patch": "x",
		"diagnosis": "d", "blast_radius": "1", "risk_level": "R2", "rollback": "r",
		"file_path": "clusters/prod/x.yaml",
	})
	if res.Err == nil {
		t.Fatal("非白名单 repo 应 fail-closed")
	}
}
