package gatekeeper

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/approval"
	"github.com/aegis-dev/aegis/internal/session"
)

var changeRequestGVR = schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "changerequests"}

// ApprovalServer 是 gatekeeper 的服务端审批入口（M2 T2.4/T2.11 软件层）。
// 安全边界：只接受 HMAC 信封；actor 来自信封（mTLS 身份绑定后续）；审批终判调用
// approval.Decide；只写 status.approval；CLI 不持有判定逻辑。
type ApprovalServer struct {
	Client   dynamic.Interface
	Verifier *session.Verifier
	Now      func() time.Time
	GVR      schema.GroupVersionResource
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
	if strings.TrimSpace(payload.Actor) == "" {
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
	if string(cr.Status.State) != string(aegisv1.StateAwaitingApproval) {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "changerequest not awaiting approval"})
		return
	}
	if !cr.Spec.Approval.Required {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "approval not required"})
		return
	}
	if cr.Status.Approval.State == "Approved" || cr.Status.Approval.State == "Denied" {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "approval already decided"})
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	req := approval.Request{ID: cr.Name, Requester: cr.Spec.RequestedBy, Required: true, State: approval.StatePending}
	decided, err := approval.Decide(req, payload.Actor, payload.Approve, now)
	if err != nil {
		status := http.StatusForbidden
		if err == approval.ErrNotRequired {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"message": err.Error()})
		return
	}
	cr.Status.Approval = aegisv1.Approval{
		Required: true, State: string(decided.State), Approver: decided.Approver,
		DecidedAt: decided.DecidedAt.UTC().Format(time.RFC3339), Channel: "cli",
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "encode changerequest failed"})
		return
	}
	if _, err := s.Client.Resource(gvr).Namespace(ns).UpdateStatus(ctx, &unstructured.Unstructured{Object: obj}, metav1.UpdateOptions{}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "update status failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": string(decided.State), "approver": decided.Approver, "message": payload.Note})
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
