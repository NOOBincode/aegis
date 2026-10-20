package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/aegis-dev/aegis/internal/session"
)

func TestApprovalCommandSignedRequest(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	t.Setenv("AEGIS_APPROVAL_SECRET", base64.RawURLEncoding.EncodeToString(secret))
	var got approvalPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/approval" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Envelope string `json:"envelope"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("body: %v", err)
		}
		v, _ := session.NewVerifier(secret, time.Minute)
		payload, err := v.Open(body.Envelope)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"state":"Approved","approver":"bob","message":"ok"}`))
	}))
	defer srv.Close()

	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	cmd.SetContext(t.Context())
	if err := runApproval(cmd, "shop/payments", true, srv.URL+"/v1/approval", "ok", "bob", time.Second); err != nil {
		t.Fatal(err)
	}
	if !got.Approve || got.CR != "shop/payments" || got.Actor != "bob" {
		t.Fatalf("payload=%+v", got)
	}
}

func TestApprovalMissingSecret(t *testing.T) {
	t.Setenv("AEGIS_APPROVAL_SECRET", "")
	cmd := &cobra.Command{}
	if err := runApproval(cmd, "shop/payments", true, "http://127.0.0.1:1/v1/approval", "", "bob", time.Second); err == nil {
		t.Fatal("缺 secret 应报错")
	}
}
