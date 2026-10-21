package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStoreRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AEGIS_SESSION_DIR", dir)
	st, err := newSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	rec := newSessionRecord("http://127.0.0.1:18082/", "kagent")
	rec.Entries = []tuiEntry{{role: tuiRoleYou, text: "hello"}, {role: tuiRoleAegis, text: "hi"}}
	if err := st.save(rec); err != nil {
		t.Fatal(err)
	}
	got, err := st.load(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 || got.Entries[0].role != tuiRoleYou || got.Entries[0].text != "hello" {
		t.Fatalf("entries=%+v", got.Entries)
	}
	info, err := os.Stat(filepath.Join(dir, rec.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode=%o", info.Mode().Perm())
	}
	list, err := st.list(10)
	if err != nil || len(list) != 1 || list[0].ID != rec.ID {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}

func TestSessionStoreRejectsTraversalAndBadID(t *testing.T) {
	st, _ := newSessionStoreFromDir(t.TempDir())
	if _, err := st.load("../secret"); err == nil {
		t.Fatal("路径穿越应拒绝")
	}
	if _, err := st.load("ABCDEF12"); err == nil {
		t.Fatal("非法 ID 应拒绝")
	}
}

func TestBuildResumeContext(t *testing.T) {
	entries := []tuiEntry{
		{role: tuiRoleSystem, text: "welcome"},
		{role: tuiRoleYou, text: "diagnose payments"},
		{role: tuiRoleAegis, text: "OOM suspected"},
	}
	ctx := buildResumeContext("deadbeef", entries)
	if ctx == "" || !containsAll(ctx, "deadbeef", "diagnose payments", "OOM suspected") {
		t.Fatalf("ctx=%q", ctx)
	}
}

func TestResumeSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AEGIS_SESSION_DIR", dir)
	st, _ := newSessionStore()
	rec := newSessionRecord("http://old:1/", "shop")
	rec.Entries = []tuiEntry{{role: tuiRoleYou, text: "old question"}, {role: tuiRoleAegis, text: "old answer"}}
	_ = st.save(rec)
	cmd := newRootCmd()
	ui := newTUI(cmd)
	if err := ui.resumeSession(rec.ID); err != nil {
		t.Fatal(err)
	}
	if ui.endpoint != "http://old:1/" || ui.namespace != "shop" || len(ui.entries) != 2 || !ui.resumePending {
		t.Fatalf("ui endpoint=%s ns=%s entries=%d pending=%v", ui.endpoint, ui.namespace, len(ui.entries), ui.resumePending)
	}
	if deriveTitle(ui.entries) != "old question" {
		t.Fatalf("title=%q", deriveTitle(ui.entries))
	}
	_ = time.Now()
}

func newSessionStoreFromDir(dir string) (*sessionStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &sessionStore{dir: dir}, nil
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !contains(s, x) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
