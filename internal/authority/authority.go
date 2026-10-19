// Package authority 实现字段级所有权图谱与变更归因的纯领域部分（M2 T2.9，I9）。
//
// 事实源在集群（managedFields/ownerReferences/控制器引用）；本包只做确定性
// 图查询、三态判定、意图翻译与归因。零 LLM、零 k8s import；集群读取在
// internal/adapters/k8s 的 port 后面。
package authority

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// OwnerKind 是字段所有者类型。
type OwnerKind string

// OwnerHPA 是 HPA 所有权；其他 owner 类型按同一图谱边表达。
const (
	OwnerHPA      OwnerKind = "hpa"
	OwnerVPA      OwnerKind = "vpa"
	OwnerKEDA     OwnerKind = "keda"
	OwnerArgoCD   OwnerKind = "argocd"
	OwnerOperator OwnerKind = "operator"
	OwnerAegis    OwnerKind = "aegis"
	OwnerUnknown  OwnerKind = "unknown"
)

// FieldRef 是字段级所有权键。
type FieldRef struct {
	Kind      string
	Namespace string
	Name      string
	Field     string
}

func (f FieldRef) key() string {
	return strings.ToLower(f.Kind) + "/" + f.Namespace + "/" + f.Name + "/" + f.Field
}

// Owner 是一条所有权声明。
type Owner struct {
	Kind     OwnerKind
	Ref      string
	Source   string
	LastSeen time.Time
}

// CheckDecision 是三态判定（§4.2.4）。
type CheckDecision string

// DecisionClear 是无主可直写；ViaOwnerAPI 需翻译；Rejected 拒绝。
const (
	DecisionClear       CheckDecision = "clear"
	DecisionViaOwnerAPI CheckDecision = "via-owner-api"
	DecisionRejected    CheckDecision = "rejected"
)

// Graph 是并发安全的字段所有权图谱；可重建派生态，不持久化。
type Graph struct {
	mu     sync.RWMutex
	owners map[string][]Owner
	clear  map[string]bool
}

// NewGraph 构造空图谱。
func NewGraph() *Graph {
	return &Graph{owners: make(map[string][]Owner), clear: make(map[string]bool)}
}

// AddOwner 增加一条所有权；同一 owner/source 覆盖 lastSeen。
func (g *Graph) AddOwner(f FieldRef, o Owner) {
	if o.Kind == "" {
		o.Kind = OwnerUnknown
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	k := f.key()
	for i := range g.owners[k] {
		if g.owners[k][i].Kind == o.Kind && g.owners[k][i].Ref == o.Ref && g.owners[k][i].Source == o.Source {
			g.owners[k][i] = o
			return
		}
	}
	g.owners[k] = append(g.owners[k], o)
	sort.SliceStable(g.owners[k], func(i, j int) bool {
		if g.owners[k][i].Kind == g.owners[k][j].Kind {
			return g.owners[k][i].Ref < g.owners[k][j].Ref
		}
		return g.owners[k][i].Kind < g.owners[k][j].Kind
	})
}

// MarkClear 显式标记字段无主（需来自独立数据源的确定性证据，不能由调用方拍脑袋）。
func (g *Graph) MarkClear(f FieldRef) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clear[f.key()] = true
}

// Owners 返回字段当前 owner 副本。
func (g *Graph) Owners(f FieldRef) []Owner {
	g.mu.RLock()
	defer g.mu.RUnlock()
	src := g.owners[f.key()]
	out := make([]Owner, len(src))
	copy(out, src)
	return out
}

// Check 执行 authorityCheck：有 owner → via-owner-api；显式 clear → clear；
// 无任何证据 → via-owner-api（保守，不轻易放行）；无法翻译 → rejected 由上层判断。
func (g *Graph) Check(f FieldRef) (CheckDecision, []Owner, error) {
	if f.Kind == "" || f.Namespace == "" || f.Name == "" || f.Field == "" {
		return DecisionRejected, nil, fmt.Errorf("FieldRef 缺 kind/namespace/name/field")
	}
	owners := g.Owners(f)
	if len(owners) > 0 {
		return DecisionViaOwnerAPI, owners, nil
	}
	g.mu.RLock()
	explicitClear := g.clear[f.key()]
	g.mu.RUnlock()
	if explicitClear {
		return DecisionClear, nil, nil
	}
	return DecisionViaOwnerAPI, nil, nil
}

// Intent 是 Agent/用户意图的确定性枚举。
type Intent string

// IntentScale 是副本意图；Resources/ Restart/ Generic 走各自翻译规则。
const (
	IntentScale     Intent = "scale"
	IntentResources Intent = "resources"
	IntentRestart   Intent = "restart"
	IntentGeneric   Intent = "generic"
)

// Translation 是意图翻译结果；Reject=true 时不得直写。
type Translation struct {
	Action string
	Owner  OwnerKind
	Reject bool
	Reason string
}

// Translate 按 owner 类型把意图翻译为对 owner 自身 API 的动作（§4.3 规则表）。
func Translate(intent Intent, owners []Owner) Translation {
	if len(owners) == 0 {
		return Translation{Owner: OwnerUnknown, Reject: true, Reason: "无所有权证据，fail-closed 拒绝直写（需人工确认）"}
	}
	o := owners[0]
	for _, cand := range owners {
		if cand.Kind == OwnerArgoCD {
			o = cand
			break
		}
	}
	switch o.Kind {
	case OwnerHPA, OwnerKEDA:
		if intent == IntentScale {
			return Translation{Action: "patch_hpa_minReplicas", Owner: o.Kind, Reason: "replicas 归 HPA/KEDA 管，改 minReplicas 而非 Deployment.replicas"}
		}
		return Translation{Owner: o.Kind, Reject: true, Reason: "HPA/KEDA 所有者不支持该意图，拒绝直写"}
	case OwnerVPA:
		if intent == IntentResources {
			return Translation{Action: "patch_vpa_policy", Owner: o.Kind, Reason: "resources 归 VPA 管，改 VPA 策略"}
		}
		return Translation{Owner: o.Kind, Reject: true, Reason: "VPA 所有者不支持该意图，拒绝直写"}
	case OwnerArgoCD:
		return Translation{Action: "open_gitops_pr", Owner: o.Kind, Reason: "ArgoCD 管辖资源一律走 GitOps PR（R2）"}
	case OwnerAegis:
		return Translation{Action: "direct_patch", Owner: o.Kind, Reason: "aegis 自管字段可走闸门直写（仍过下游检查）"}
	default:
		return Translation{Owner: o.Kind, Reject: true, Reason: "未知所有者，不猜翻译路径"}
	}
}

// Change 是 Agent 感知的一次状态变化。
type Change struct {
	Field      FieldRef
	ObservedAt time.Time
	Direction  string // increase|decrease|replace
	Magnitude  int
}

// Evidence 是控制器行为证据（Events/status/audit）。
type Evidence struct {
	Controller string
	At         time.Time
	Direction  string
	Magnitude  int
	Source     string
}

// Verdict 是归因结论。
type Verdict string

// VerdictAttributed 表示变化可归因到健康控制器，应视为非事件。
const (
	VerdictAttributed   Verdict = "attributed"
	VerdictUnattributed Verdict = "unattributed"
	VerdictPartial      Verdict = "partial"
)

// Attribution 是归因输出。
type Attribution struct {
	Verdict    Verdict
	Controller string
	Reasons    []string
}

// Attribute 执行确定性归因：变化落在 owner 控制器动作窗口内且方向一致 → attributed；
// 有证据但幅度/方向不吻合 → partial；无证据 → unattributed。宁可 partial 也不硬归因。
func Attribute(ch Change, owners []Owner, evidence []Evidence, window time.Duration) Attribution {
	if len(owners) == 0 {
		return Attribution{Verdict: VerdictUnattributed, Reasons: []string{"无 owner，进入残差"}}
	}
	if window <= 0 {
		window = 2 * time.Minute
	}
	ownerKinds := make(map[string]bool, len(owners))
	for _, o := range owners {
		ownerKinds[string(o.Kind)] = true
	}
	relevant := 0
	mismatch := 0
	controller := ""
	for _, e := range evidence {
		if !within(ch.ObservedAt, e.At, window) {
			continue
		}
		if !ownerKinds[strings.ToLower(e.Controller)] && !strings.EqualFold(e.Controller, "controller") {
			continue
		}
		relevant++
		controller = e.Controller
		if e.Direction == "" || ch.Direction == "" || strings.EqualFold(e.Direction, ch.Direction) {
			// 幅度缺失视为部分证据；幅度差 > max(1, 20%) 视为 mismatch。
			if ch.Magnitude > 0 && e.Magnitude > 0 {
				diff := ch.Magnitude - e.Magnitude
				if diff < 0 {
					diff = -diff
				}
				threshold := ch.Magnitude / 5
				if threshold < 1 {
					threshold = 1
				}
				if diff > threshold {
					mismatch++
					continue
				}
			}
			return Attribution{Verdict: VerdictAttributed, Controller: controller, Reasons: []string{"变化落在 owner 动作窗口且方向/幅度一致"}}
		}
		mismatch++
	}
	if relevant > 0 {
		return Attribution{Verdict: VerdictPartial, Controller: controller, Reasons: []string{fmt.Sprintf("有 %d 条 owner 证据但 %d 条不吻合", relevant, mismatch)}}
	}
	return Attribution{Verdict: VerdictUnattributed, Reasons: []string{"无 owner 控制器动作证据"}}
}

func within(observed, event time.Time, window time.Duration) bool {
	if observed.IsZero() || event.IsZero() {
		return false
	}
	d := observed.Sub(event)
	if d < 0 {
		d = -d
	}
	return d <= window
}
