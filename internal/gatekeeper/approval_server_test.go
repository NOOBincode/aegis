package gatekeeper

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/aegis-dev/aegis/internal/session"
)

func crObject(state string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aegis.dev/v1alpha1", "kind": "ChangeRequest",
		"metadata": map[string]any{"name": "cr-1", "namespace": "shop", "generation": int64(1)},
		"spec": map[string]any{
			"sessionRef": "s", "requestedBy": "alice", "idempotencyKey": "k", "intent": "i", "source": "agent",
			"approval":   map[string]any{"required": true},
			"operations": []any{map[string]any{"tool": "scale_deployment", "riskLevel": "R2"}},
		},
		"status": map[string]any{"state": state},
	}}
}

func approvalFixture(t *testing.T, state string) (*ApprovalServer, []byte, string) {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := session.NewSigner(secret, time.Minute)
	verifier, _ := session.NewVerifier(secret, time.Minute)
	obj := crObject(state)
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{changeRequestGVR: "ChangeRequestList"}, obj)
	token, err := signer.Seal("cli-1", 1, mustJSON(t, approvalEnvelopePayload{CR: "shop/cr-1", Approve: true, Actor: "bob", Note: "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	return &ApprovalServer{Client: dc, Verifier: verifier, GVR: changeRequestGVR, Now: func() time.Time { return time.Unix(100, 0) }}, secret, token
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApprovalServerApproves(t *testing.T) {
	srv, _, token := approvalFixture(t, "AwaitingApproval")
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	u, err := srv.Client.Resource(changeRequestGVR).Namespace("shop").Get(req.Context(), "cr-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := unstructured.NestedString(u.Object, "status", "approval", "state")
	if state != "Approved" {
		t.Fatalf("status approval=%q object=%v", state, u.Object["status"])
	}
}

func TestApprovalServerRejectsSelfApproval(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := session.NewSigner(secret, time.Minute)
	verifier, _ := session.NewVerifier(secret, time.Minute)
	obj := crObject("AwaitingApproval")
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{changeRequestGVR: "ChangeRequestList"}, obj)
	srv := &ApprovalServer{Client: dc, Verifier: verifier, GVR: changeRequestGVR}
	token, _ := signer.Seal("cli-1", 1, mustJSON(t, approvalEnvelopePayload{CR: "shop/cr-1", Approve: true, Actor: "alice"}))
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("self approval code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestApprovalServerWrongState(t *testing.T) {
	srv, _, token := approvalFixture(t, "Executing")
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("wrong state code=%d body=%s", w.Code, w.Body.String())
	}
}
