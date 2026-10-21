package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatOneShot(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Message struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got = req.Params.Message.Parts[0].Text
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"kind":"task","artifacts":[{"parts":[{"kind":"text","text":"诊断结论：all good"}]}]}}`))
	}))
	defer srv.Close()
	cmd := newChatCmd()
	cmd.SetArgs([]string{"--endpoint", srv.URL, "--message", "hello"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got != "hello" || !strings.Contains(out.String(), "all good") {
		t.Fatalf("got=%q out=%q", got, out.String())
	}
}

func TestChatInteractive(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"status":{"message":{"parts":[{"kind":"text","text":"reply"}]}}}}`))
	}))
	defer srv.Close()
	cmd := newChatCmd()
	cmd.SetArgs([]string{"--endpoint", srv.URL})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("hi\n/exit\n"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.Contains(out.String(), "reply") {
		t.Fatalf("count=%d out=%q", count, out.String())
	}
}

func TestSendA2AErrorAndTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := sendA2AMessage(context.Background(), srv.URL, time.Second, "hi"); err == nil {
		t.Fatal("非 2xx 应失败")
	}
}
