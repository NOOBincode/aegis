package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aegis-dev/aegis/internal/authority"
)

// AuthorityProvider 从集群事实构建字段所有权图谱（M2 T2.9）。
// 只读 list Deployments/StatefulSets/HPAs；不读 Secret；不做写。
type AuthorityProvider struct {
	cs kubernetes.Interface
}

// NewAuthorityProvider 构造只读所有权图谱数据源。
func NewAuthorityProvider(cs kubernetes.Interface) *AuthorityProvider {
	return &AuthorityProvider{cs: cs}
}

// BuildGraph 从 HPA scaleTargetRef、ArgoCD 追踪注解、managedFields 的 manager 证据
// 构建图谱。无证据字段保持 Graph.Check 的保守 via-owner-api。
func (p *AuthorityProvider) BuildGraph(ctx context.Context, namespaces []string) (*authority.Graph, error) {
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	g := authority.NewGraph()
	for _, ns := range namespaces {
		if err := p.addWorkloads(ctx, g, ns); err != nil {
			return nil, err
		}
		if err := p.addHPAs(ctx, g, ns); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func (p *AuthorityProvider) addWorkloads(ctx context.Context, g *authority.Graph, ns string) error {
	deps, err := p.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}
	for i := range deps.Items {
		d := &deps.Items[i]
		if o := ownerFromObject(d.Labels, d.Annotations, d.ManagedFields); o.Kind != authority.OwnerUnknown {
			g.AddOwner(authority.FieldRef{Kind: "Deployment", Namespace: d.Namespace, Name: d.Name, Field: "spec.replicas"}, o)
		}
	}
	sts, err := p.cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list statefulsets: %w", err)
	}
	for i := range sts.Items {
		s := &sts.Items[i]
		if o := ownerFromObject(s.Labels, s.Annotations, s.ManagedFields); o.Kind != authority.OwnerUnknown {
			g.AddOwner(authority.FieldRef{Kind: "StatefulSet", Namespace: s.Namespace, Name: s.Name, Field: "spec.replicas"}, o)
		}
	}
	return nil
}

func (p *AuthorityProvider) addHPAs(ctx context.Context, g *authority.Graph, ns string) error {
	hpas, err := p.cs.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list hpas: %w", err)
	}
	for i := range hpas.Items {
		h := &hpas.Items[i]
		ref := h.Spec.ScaleTargetRef
		if ref.Kind == "" || ref.Name == "" {
			continue
		}
		if ref.Kind != "Deployment" && ref.Kind != "StatefulSet" {
			continue
		}
		g.AddOwner(authority.FieldRef{Kind: ref.Kind, Namespace: h.Namespace, Name: ref.Name, Field: "spec.replicas"},
			authority.Owner{Kind: authority.OwnerHPA, Ref: "hpa/" + h.Name, Source: "hpa.scaleTargetRef", LastSeen: h.CreationTimestamp.Time})
	}
	return nil
}

func ownerFromObject(labels, annotations map[string]string, managers []metav1.ManagedFieldsEntry) authority.Owner {
	for k := range labels {
		if strings.Contains(strings.ToLower(k), "argocd.argoproj.io") {
			return authority.Owner{Kind: authority.OwnerArgoCD, Ref: k, Source: "label"}
		}
	}
	for k := range annotations {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "argocd.argoproj.io") || strings.Contains(lk, "kubectl.kubernetes.io/last-applied") {
			return authority.Owner{Kind: authority.OwnerArgoCD, Ref: k, Source: "annotation"}
		}
	}
	for _, m := range managers {
		mg := strings.ToLower(m.Manager)
		switch {
		case strings.Contains(mg, "argocd"):
			return authority.Owner{Kind: authority.OwnerArgoCD, Ref: m.Manager, Source: "managedFields"}
		case strings.Contains(mg, "hpa"), strings.Contains(mg, "horizontal-pod-autoscaler"):
			return authority.Owner{Kind: authority.OwnerHPA, Ref: m.Manager, Source: "managedFields"}
		case strings.Contains(mg, "aegis"):
			return authority.Owner{Kind: authority.OwnerAegis, Ref: m.Manager, Source: "managedFields"}
		}
	}
	return authority.Owner{Kind: authority.OwnerUnknown, Ref: "", Source: "no-evidence"}
}

// Events 实现 authority.EvidenceSource：只读 K8s Events，按目标对象与时间窗过滤，
// 把 source.component 归一化到 ownerKind（hpa/vpa/argocd/operator），并尽力解析方向。
// 方向缺失时 Attribute 会降为 partial，不会硬归因。
func (p *AuthorityProvider) Events(ctx context.Context, ref authority.FieldRef, since, until time.Time) ([]authority.Evidence, error) {
	list, err := p.cs.CoreV1().Events(ref.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	var out []authority.Evidence
	for i := range list.Items {
		e := &list.Items[i]
		if !strings.EqualFold(e.InvolvedObject.Kind, ref.Kind) || !strings.EqualFold(e.InvolvedObject.Name, ref.Name) {
			continue
		}
		at := eventTime(e)
		if at.Before(since) || at.After(until) {
			continue
		}
		ctrl := normalizeController(e.Source.Component)
		if ctrl == "" {
			continue
		}
		out = append(out, authority.Evidence{
			Controller: ctrl, At: at, Direction: eventDirection(e), Source: "k8s-event:" + e.Reason,
		})
	}
	return out, nil
}

func normalizeController(component string) string {
	c := strings.ToLower(component)
	switch {
	case strings.Contains(c, "horizontal-pod-autoscaler"), strings.Contains(c, "hpa"):
		return "hpa"
	case strings.Contains(c, "vertical-pod-autoscaler"), strings.Contains(c, "vpa"):
		return "vpa"
	case strings.Contains(c, "argocd"):
		return "argocd"
	case strings.Contains(c, "keda"):
		return "keda"
	case strings.Contains(c, "operator"):
		return "operator"
	}
	return ""
}

func eventDirection(e *corev1.Event) string {
	text := strings.ToLower(e.Reason + " " + e.Message)
	switch {
	case strings.Contains(text, "down"), strings.Contains(text, "decrease"), strings.Contains(text, "delete"):
		return "decrease"
	case strings.Contains(text, "up"), strings.Contains(text, "increase"), strings.Contains(text, "create"), strings.Contains(text, "rescale"):
		return "increase"
	}
	return ""
}
