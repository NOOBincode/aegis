package gatekeeper

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/approval"
	"github.com/aegis-dev/aegis/internal/session"
)

func crObject(state string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aegis.dev/v1alpha1", "kind": "ChangeRequest",
		"metadata": map[string]any{"name": "cr-1", "namespace": "shop", "generation": int64(1), "uid": "uid-cr-1"},
		"spec": map[string]any{
			"sessionRef": "s", "requestedBy": "alice", "idempotencyKey": "k", "intent": "i", "source": "agent",
			"approval":   map[string]any{"required": true},
			"operations": []any{map[string]any{"tool": "scale_deployment", "riskLevel": "R2"}},
		},
		"status": map[string]any{"state": state},
	}}
}

func approvalFixture(t *testing.T, state string) (*ApprovalServer, string) {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := session.NewSigner(secret, time.Minute)
	verifier, _ := session.NewVerifier(secret, time.Minute)
	obj := crObject(state)
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		changeRequestGVR: "ChangeRequestList", approvalDecisionGVR: "ApprovalDecisionList",
	}, obj)
	token, err := signer.Seal("cli-1", 1, mustJSON(t, approvalEnvelopePayload{CR: "shop/cr-1", Approve: true, Actor: "bob", Note: "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	return &ApprovalServer{Client: dc, Verifier: verifier, GVR: changeRequestGVR, Now: func() time.Time { return time.Unix(100, 0) }}, token
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
	srv, token := approvalFixture(t, "AwaitingApproval")
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	u, err := srv.Client.Resource(approvalDecisionGVR).Namespace("shop").Get(req.Context(), approvalDecisionName("uid-cr-1"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path []string
		want string
	}{
		{path: []string{"spec", "changeRequestRef", "uid"}, want: "uid-cr-1"},
		{path: []string{"spec", "approver"}, want: "bob"},
		{path: []string{"spec", "decision"}, want: "Approved"},
		{path: []string{"spec", "channel"}, want: "cli"},
	} {
		got, _, _ := unstructured.NestedString(u.Object, check.path...)
		if got != check.want {
			t.Errorf("field %v=%q, want %q", check.path, got, check.want)
		}
	}
	hash, _, _ := unstructured.NestedString(u.Object, "spec", "evidenceHash")
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("evidenceHash=%q", hash)
	}
	owners, _, err := unstructured.NestedSlice(u.Object, "metadata", "ownerReferences")
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 {
		t.Fatalf("ownerReferences=%v", owners)
	}
	owner, ok := owners[0].(map[string]any)
	if !ok || owner["uid"] != "uid-cr-1" {
		t.Fatalf("ownerReferences=%v", owners)
	}
	cr, err := srv.Client.Resource(changeRequestGVR).Namespace("shop").Get(req.Context(), "cr-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := unstructured.NestedString(cr.Object, "status", "approval", "state")
	if state == "Approved" {
		t.Fatalf("gatekeeper must not write ChangeRequest status: %v", cr.Object["status"])
	}
}

func TestSameApprovalDecisionRequiresFullRef(t *testing.T) {
	cr := &aegisv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "cr-1", Namespace: "shop", UID: "uid-cr-1"}}
	decided := approval.Request{Approver: "bob", State: approval.StateApproved, DecidedAt: time.Unix(100, 0)}
	want, err := buildApprovalDecision(cr, decided, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{name: "namespace", field: "namespace", value: "other"},
		{name: "name", field: "name", value: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := want.DeepCopy()
			if err := unstructured.SetNestedField(got.Object, test.value, "spec", "changeRequestRef", test.field); err != nil {
				t.Fatal(err)
			}
			if sameApprovalDecision(got, want) {
				t.Fatalf("changed ref %s was accepted", test.field)
			}
		})
	}
}

func TestApprovalServerDecisionIsIdempotent(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := session.NewSigner(secret, time.Minute)
	verifier, _ := session.NewVerifier(secret, time.Minute)
	obj := crObject("AwaitingApproval")
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		changeRequestGVR: "ChangeRequestList", approvalDecisionGVR: "ApprovalDecisionList",
	}, obj)
	srv := &ApprovalServer{Client: dc, Verifier: verifier, GVR: changeRequestGVR}
	payload := approvalEnvelopePayload{CR: "shop/cr-1", Approve: true, Actor: "bob", Note: "ok"}
	for _, seq := range []uint64{1, 2} {
		token, err := signer.Seal("cli-1", seq, mustJSON(t, payload))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]string{"envelope": token})
		req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("seq=%d code=%d body=%s", seq, w.Code, w.Body.String())
		}
		if seq == 1 {
			u, err := srv.Client.Resource(changeRequestGVR).Namespace("shop").Get(context.Background(), "cr-1", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			u.Object["status"] = map[string]any{
				"state":    "Committed",
				"approval": map[string]any{"state": "Approved", "approver": "bob"},
			}
			if _, err := srv.Client.Resource(changeRequestGVR).Namespace("shop").UpdateStatus(context.Background(), u, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	list, err := srv.Client.Resource(approvalDecisionGVR).Namespace("shop").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("decisions=%d, want one idempotent object", len(list.Items))
	}
}

func TestApprovalServerRejectsConflictingExistingDecision(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := session.NewSigner(secret, time.Minute)
	verifier, _ := session.NewVerifier(secret, time.Minute)
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aegis.dev/v1alpha1", "kind": "ApprovalDecision",
		"metadata": map[string]any{"name": approvalDecisionName("uid-cr-1"), "namespace": "shop"},
		"spec": map[string]any{
			"changeRequestRef": map[string]any{"namespace": "shop", "name": "cr-1", "uid": "uid-cr-1"},
			"approver":         "mallory", "decision": "Approved", "evidenceHash": "sha256:forged",
		},
	}}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		changeRequestGVR: "ChangeRequestList", approvalDecisionGVR: "ApprovalDecisionList",
	}, crObject("AwaitingApproval"), existing)
	srv := &ApprovalServer{Client: dc, Verifier: verifier, GVR: changeRequestGVR}
	token, err := signer.Seal("cli-1", 1, mustJSON(t, approvalEnvelopePayload{CR: "shop/cr-1", Approve: true, Actor: "bob", Note: "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("conflicting decision code=%d body=%s", w.Code, w.Body.String())
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
	srv, token := approvalFixture(t, "Executing")
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("wrong state code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestApprovalServerRequiresCertActorWhenConfigured(t *testing.T) {
	srv, token := approvalFixture(t, "AwaitingApproval")
	srv.ActorFromCert = true
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无客户端证书应 401，得到 %d：%s", w.Code, w.Body.String())
	}
}

func TestApprovalServerRejectsActorCertMismatch(t *testing.T) {
	srv, token := approvalFixture(t, "AwaitingApproval")
	srv.ActorFromCert = true
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: "carol"}}}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("payload actor bob vs cert carol 应 403，得到 %d：%s", w.Code, w.Body.String())
	}
}

func TestApprovalServerRejectsReplay(t *testing.T) {
	srv, token := approvalFixture(t, "AwaitingApproval")
	body, _ := json.Marshal(map[string]string{"envelope": token})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first approval code=%d body=%s", w.Code, w.Body.String())
	}
	req2 := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("重放应以 401 拒绝，得到 %d：%s", w2.Code, w2.Body.String())
	}
}

func TestApprovalServerRejectsTamperedEnvelope(t *testing.T) {
	srv, token := approvalFixture(t, "AwaitingApproval")
	if token == "" {
		t.Fatal("token 为空")
	}
	tampered := token[:len(token)-1] + "x"
	body, _ := json.Marshal(map[string]string{"envelope": tampered})
	req := httptest.NewRequest(http.MethodPost, "/v1/approval", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("篡改信封应 401，得到 %d：%s", w.Code, w.Body.String())
	}
}
