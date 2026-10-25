package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/aegis-dev/aegis/internal/gatekeeper"
	"github.com/aegis-dev/aegis/internal/session"
)

// CLI 签名提交 → 真实 ApprovalServer 验签/状态机 → 创建 ApprovalDecision 的端到端。
func TestCLIApprovalEndToEnd(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	t.Setenv("AEGIS_APPROVAL_SECRET", base64.RawURLEncoding.EncodeToString(secret))
	gvr := schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "changerequests"}
	decisionGVR := schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "approvaldecisions"}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aegis.dev/v1alpha1", "kind": "ChangeRequest",
		"metadata": map[string]any{"name": "cr-1", "namespace": "shop", "generation": int64(1), "uid": "uid-cr-1"},
		"spec": map[string]any{
			"sessionRef": "s", "requestedBy": "alice", "idempotencyKey": "k", "intent": "i", "source": "agent",
			"approval":   map[string]any{"required": true},
			"operations": []any{map[string]any{"tool": "scale_deployment", "riskLevel": "R2"}},
		},
		"status": map[string]any{"state": "AwaitingApproval"},
	}}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "ChangeRequestList", decisionGVR: "ApprovalDecisionList",
	}, obj)
	verifier, _ := session.NewVerifier(secret, time.Minute)
	srv := httptest.NewServer(&gatekeeper.ApprovalServer{Client: dc, Verifier: verifier, GVR: gvr, Now: time.Now})
	defer srv.Close()

	cmd := &cobra.Command{}
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetContext(t.Context())
	if err := runApproval(cmd, "shop/cr-1", true, srv.URL, "ok", "bob", time.Second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Approved") || !strings.Contains(out.String(), "bob") {
		t.Fatalf("out=%q", out.String())
	}
	list, err := dc.Resource(decisionGVR).Namespace("shop").List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("decisions=%d, want 1", len(list.Items))
	}
	state, _, _ := unstructured.NestedString(list.Items[0].Object, "spec", "decision")
	if state != "Approved" {
		t.Fatalf("decision=%q", state)
	}
	u, err := dc.Resource(gvr).Namespace("shop").Get(t.Context(), "cr-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ = unstructured.NestedString(u.Object, "status", "approval", "state")
	if state == "Approved" {
		b, _ := json.Marshal(u.Object["status"])
		t.Fatalf("approval service must not write ChangeRequest status: %s", b)
	}
}
