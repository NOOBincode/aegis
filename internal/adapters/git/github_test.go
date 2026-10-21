package git

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreatePullRequest(t *testing.T) {
	var got struct {
		Title, Head, Base, Body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/gitops/pulls" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("auth=%q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/org/gitops/pull/1"}`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "tok", Owner: "org", Repo: "gitops"})
	if err != nil {
		t.Fatal(err)
	}
	url, err := c.CreatePullRequest(context.Background(), PullRequest{Title: "aegis: cr-1", Head: "aegis/cr-1", Base: "main", Body: "body"})
	if err != nil || !strings.Contains(url, "/pull/1") {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if got.Head != "aegis/cr-1" || got.Base != "main" || got.Title == "" {
		t.Fatalf("payload=%+v", got)
	}
}

func TestCreatePullRequestFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, Owner: "org", Repo: "gitops"})
	if _, err := c.CreatePullRequest(context.Background(), PullRequest{Title: "t", Head: "h"}); err == nil {
		t.Fatal("422 应失败")
	}
	if _, err := c.CreatePullRequest(context.Background(), PullRequest{}); err == nil {
		t.Fatal("空 head/title 应失败")
	}
}

func TestEnsureBranchAndPutFile(t *testing.T) {
	var sawRef, sawFile bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/gitops/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base-sha"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/gitops/git/refs":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["ref"] != "refs/heads/aegis/cr-1" || body["sha"] != "base-sha" {
				t.Fatalf("ref body=%v", body)
			}
			sawRef = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/gitops/contents/clusters/prod/app.yaml":
			http.NotFound(w, r)
		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/gitops/contents/clusters/prod/app.yaml":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["branch"] != "aegis/cr-1" || body["content"] == "" || body["sha"] != "" {
				t.Fatalf("file body=%v", body)
			}
			sawFile = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, Owner: "org", Repo: "gitops"})
	if err := c.EnsureBranch(context.Background(), "aegis/cr-1", "main"); err != nil {
		t.Fatal(err)
	}
	if err := c.PutFile(context.Background(), "aegis/cr-1", "clusters/prod/app.yaml", []byte("kind: ConfigMap\n"), "aegis: cr-1"); err != nil {
		t.Fatal(err)
	}
	if !sawRef || !sawFile {
		t.Fatalf("ref=%v file=%v", sawRef, sawFile)
	}
}
