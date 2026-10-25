package gatekeeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/approval"
	"github.com/aegis-dev/aegis/internal/redact"
	"github.com/aegis-dev/aegis/internal/session"
)

var changeRequestGVR = schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "changerequests"}
var approvalDecisionGVR = schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "approvaldecisions"}

// ApprovalServer 是 gatekeeper 的服务端审批入口（M2 T2.4/T2.11 软件层）。
// 安全边界：只接受 HMAC 信封；启用 ActorFromCert 时 actor 必须匹配 mTLS 证书 CN，
// 否则来自已认证信封；审批终判调用 approval.Decide；只创建不可变 ApprovalDecision；
// ChangeRequest status 由 controller 独占。
type ApprovalServer struct {
	Client        dynamic.Interface
	Verifier      *session.Verifier
	Now           func() time.Time
	GVR           schema.GroupVersionResource
	ActorFromCert bool // true 时强制用 mTLS 客户端证书 CN，忽略/校验 payload actor
}

type approvalEnvelopePayload struct {
	CR      string `json:"cr"`
	Approve bool   `json:"approve"`
	Note    string `json:"note,omitempty"`
	Actor   string `json:"actor"`
}

func (s *ApprovalServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}
	if s.Client == nil || s.Verifier == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "approval server 未装配"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "read body failed"})
		return
	}
	var envelope struct {
		Envelope string `json:"envelope"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Envelope == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "envelope required"})
		return
	}
	raw, err := s.Verifier.Open(envelope.Envelope)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "envelope rejected: " + err.Error()})
		return
	}
	var payload approvalEnvelopePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "payload invalid"})
		return
	}
	ns, name, ok := splitCR(payload.CR)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "cr must be namespace/name"})
		return
	}
	actor := strings.TrimSpace(payload.Actor)
	if s.ActorFromCert {
		certActor := peerActor(r)
		if certActor == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "mTLS client certificate required"})
			return
		}
		if actor != "" && actor != certActor {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "actor does not match client certificate"})
			return
		}
		actor = certActor
	}
	if actor == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "actor missing"})
		return
	}
	gvr := s.GVR
	if gvr.Resource == "" {
		gvr = changeRequestGVR
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	u, err := s.Client.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "changerequest not found"})
		return
	}
	cr := &aegisv1.ChangeRequest{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "decode changerequest failed"})
		return
	}
	if !cr.DeletionTimestamp.IsZero() {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "changerequest deleting"})
		return
	}
	if !cr.Spec.Approval.Required {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "approval not required"})
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	req := approval.Request{ID: cr.Name, Requester: cr.Spec.RequestedBy, Required: true, State: approval.StatePending}
	decided, err := approval.Decide(req, actor, payload.Approve, now)
	if err != nil {
		status := http.StatusForbidden
		if err == approval.ErrNotRequired {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"message": err.Error()})
		return
	}
	decision, err := buildApprovalDecision(cr, decided, payload.Note)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "build approval decision failed: " + err.Error()})
		return
	}
	existing, err := s.Client.Resource(approvalDecisionGVR).Namespace(ns).Get(ctx, decision.GetName(), metav1.GetOptions{})
	if err == nil {
		if !sameApprovalDecision(existing, decision) {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "approval already decided"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"state": string(decided.State), "approver": decided.Approver,
			"message": payload.Note, "decision": decision.GetName(),
		})
		return
	}
	if !apierrors.IsNotFound(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "read approval decision failed: " + err.Error()})
		return
	}
	if string(cr.Status.State) != string(aegisv1.StateAwaitingApproval) {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "changerequest not awaiting approval"})
		return
	}
	if cr.Status.Approval.State == "Approved" || cr.Status.Approval.State == "Denied" {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "approval already decided"})
		return
	}
	_, err = s.Client.Resource(approvalDecisionGVR).Namespace(ns).Create(ctx, decision, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := s.Client.Resource(approvalDecisionGVR).Namespace(ns).Get(ctx, decision.GetName(), metav1.GetOptions{})
		if getErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "read existing approval decision failed: " + getErr.Error()})
			return
		}
		if !sameApprovalDecision(existing, decision) {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "approval already decided"})
			return
		}
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "create approval decision failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"state": string(decided.State), "approver": decided.Approver,
		"message": payload.Note, "decision": decision.GetName(),
	})
}

func buildApprovalDecision(cr *aegisv1.ChangeRequest, decided approval.Request, note string) (*unstructured.Unstructured, error) {
	if cr.UID == "" {
		return nil, fmt.Errorf("changerequest uid missing")
	}
	note = sanitizeApprovalNote(note)
	evidenceHash, err := approvalEvidenceHash(cr, note)
	if err != nil {
		return nil, err
	}
	controller := true
	decision := &aegisv1.ApprovalDecision{
		TypeMeta: metav1.TypeMeta{APIVersion: "aegis.dev/v1alpha1", Kind: "ApprovalDecision"},
		ObjectMeta: metav1.ObjectMeta{
			Name: approvalDecisionName(cr.UID), Namespace: cr.Namespace,
			Labels: map[string]string{"aegis.dev/change-request": cr.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "aegis.dev/v1alpha1", Kind: "ChangeRequest",
				Name: cr.Name, UID: cr.UID, Controller: &controller,
			}},
		},
		Spec: aegisv1.ApprovalDecisionSpec{
			ChangeRequestRef: aegisv1.ApprovalChangeRequestRef{Namespace: cr.Namespace, Name: cr.Name, UID: string(cr.UID)},
			Approver:         decided.Approver, Decision: string(decided.State), Channel: "cli",
			DecidedAt: decided.DecidedAt.UTC().Format(time.RFC3339), EvidenceHash: evidenceHash, Note: note,
		},
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(decision)
	if err != nil {
		return nil, fmt.Errorf("encode approval decision: %w", err)
	}
	return &unstructured.Unstructured{Object: obj}, nil
}

func approvalDecisionName(uid types.UID) string {
	sum := sha256.Sum256([]byte(uid))
	return "ad-" + hex.EncodeToString(sum[:])[:16]
}

func approvalEvidenceHash(cr *aegisv1.ChangeRequest, note string) (string, error) {
	payload, err := json.Marshal(struct {
		Ref  aegisv1.ApprovalChangeRequestRef `json:"ref"`
		Spec aegisv1.ChangeRequestSpec        `json:"spec"`
		Note string                           `json:"note"`
	}{
		Ref:  aegisv1.ApprovalChangeRequestRef{Namespace: cr.Namespace, Name: cr.Name, UID: string(cr.UID)},
		Spec: cr.Spec, Note: note,
	})
	if err != nil {
		return "", fmt.Errorf("encode approval evidence: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sanitizeApprovalNote(note string) string {
	note, _ = redact.String(note)
	runes := []rune(note)
	if len(runes) > 512 {
		return string(runes[:512])
	}
	return note
}

func sameApprovalDecision(got, want *unstructured.Unstructured) bool {
	paths := [][]string{
		{"spec", "changeRequestRef", "namespace"},
		{"spec", "changeRequestRef", "name"},
		{"spec", "changeRequestRef", "uid"},
		{"spec", "approver"},
		{"spec", "decision"},
		{"spec", "evidenceHash"},
	}
	for _, path := range paths {
		gotValue, _, _ := unstructured.NestedString(got.Object, path...)
		wantValue, _, _ := unstructured.NestedString(want.Object, path...)
		if gotValue != wantValue {
			return false
		}
	}
	return true
}

func peerActor(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return strings.TrimSpace(r.TLS.PeerCertificates[0].Subject.CommonName)
}

func splitCR(cr string) (string, string, bool) {
	parts := strings.SplitN(cr, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
