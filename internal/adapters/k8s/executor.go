package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/aegis-dev/aegis/internal/rollback"
)

// ScaleExecutor 是 gatekeeper.Executor 的 scale_deployment 实现（M2 T2.8 真实写路径）。
// 只支持窄操作 scale_deployment；其余工具一律拒绝，避免“万能写”入口。
// 先 server-side dry-run，再真实 patch；验证只做期望副本数核对，健康验证由 verifier 扩展。
type ScaleExecutor struct {
	cs kubernetes.Interface
}

// NewScaleExecutor 构造 scale 执行器。
func NewScaleExecutor(cs kubernetes.Interface) *ScaleExecutor {
	return &ScaleExecutor{cs: cs}
}

// Execute 执行 scale_deployment：dry-run 通过后才真实 patch。
func (e *ScaleExecutor) Execute(ctx context.Context, op rollback.Operation) error {
	ns, name, replicas, err := scaleArgs(op)
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"replicas": replicas}})
	if _, err := e.cs.AppsV1().Deployments(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		return fmt.Errorf("dry-run scale %s/%s: %w", ns, name, err)
	}
	if _, err := e.cs.AppsV1().Deployments(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("scale %s/%s: %w", ns, name, err)
	}
	return nil
}

// Verify 核对目标 Deployment 的 spec.replicas 是否已等于期望值。
func (e *ScaleExecutor) Verify(ctx context.Context, op rollback.Operation) error {
	ns, name, replicas, err := scaleArgs(op)
	if err != nil {
		return err
	}
	d, err := e.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("verify get %s/%s: %w", ns, name, err)
	}
	got := int32(0)
	if d.Spec.Replicas != nil {
		got = *d.Spec.Replicas
	}
	if got != replicas {
		return fmt.Errorf("verify %s/%s: replicas=%d want=%d", ns, name, got, replicas)
	}
	return nil
}

// Rollback 执行逆操作（由 rollback.Generate 生成，仍是 scale_deployment）。
func (e *ScaleExecutor) Rollback(ctx context.Context, inverse rollback.Operation) error {
	return e.Execute(ctx, inverse)
}

func scaleArgs(op rollback.Operation) (string, string, int32, error) {
	if op.Tool != "scale_deployment" {
		return "", "", 0, fmt.Errorf("executor 只支持 scale_deployment，拒绝 %q", op.Tool)
	}
	ns, _ := op.Args["namespace"].(string)
	name, _ := op.Args["name"].(string)
	if ns == "" || name == "" {
		return "", "", 0, fmt.Errorf("scale 缺 namespace/name")
	}
	replicas, ok := intArg(op.Args, "replicas")
	if !ok || replicas < 0 || replicas > math.MaxInt32 {
		return "", "", 0, fmt.Errorf("scale 缺合法 replicas（0..MaxInt32）")
	}
	return ns, name, int32(replicas), nil //nolint:gosec // 已校验 0..MaxInt32
}

func intArg(args map[string]any, key string) (int, bool) {
	if args == nil {
		return 0, false
	}
	switch n := args[key].(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
