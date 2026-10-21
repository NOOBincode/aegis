package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalAppendVerifyAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, typ := range []EventType{EventInput, EventIntent, EventResult} {
		if err := j.Append(Event{Type: typ, CR: "kagent/cr-1", RequestID: "sha256:abc", Payload: []byte(`{"n":` + string(rune('1'+i)) + `}`), TS: time.Unix(int64(i), 0).UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	events := mustVerifyFile(t, path)
	if len(events) != 3 || events[2].Seq != 3 || events[2].Checksum == "" {
		t.Fatalf("events=%+v", events)
	}
	j2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j2.Close() }()
	if err := j2.Append(Event{Type: EventCheckpoint}); err != nil {
		t.Fatal(err)
	}
	if got := mustVerifyFile(t, path); len(got) != 4 || got[3].Seq != 4 {
		t.Fatalf("reopen seq wrong: %+v", got)
	}
}

func TestJournalVerifyRejectsTamperAndBadType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append(Event{Type: EventIntent, RequestID: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	raw, _ := os.ReadFile(path) //nolint:gosec // 测试临时 journal 路径
	tampered := strings.Replace(string(raw), `"request_id":"x"`, `"request_id":"y"`, 1)
	if tampered == string(raw) {
		t.Fatal("测试未篡改")
	}
	if _, err := Verify(strings.NewReader(tampered)); err == nil {
		t.Fatal("篡改应校验失败")
	}
	if err := j.Append(Event{Type: EventType("Bogus")}); err == nil {
		t.Fatal("未知类型应拒绝")
	}
}

func mustVerifyFile(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // 测试临时 journal 路径
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	events, err := Verify(f)
	if err != nil {
		t.Fatal(err)
	}
	return events
}
