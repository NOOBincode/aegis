// Package rollback 实现写操作逆操作的确定性生成（M2 T2.5，I5）。
//
// 安全边界：本包只生成/校验逆操作，不执行写；无法生成逆操作时返回
// Reversible=false，调用方（闸门）必须把该操作升级为 R2，不能自动执行。
package rollback

import (
	"fmt"
	"strings"
)

// Operation 是一个写操作的纯领域表示（与 ChangeOperation 对齐的最小字段）。
type Operation struct {
	Tool string
	Args map[string]any
}

// Result 是逆操作生成结果；Reversible=false 时 Inverse 必须为 nil。
type Result struct {
	Inverse    *Operation
	Reversible bool
	Reason     string
}

// Generate 按工具白名单生成逆操作。只支持可证明可逆的窄操作：
//   - scale_deployment/scale_statefulset：需要 from_replicas 或 replicas_from 当前值；
//   - cordon_node：逆操作为 uncordon_node；
//   - 其余（restart_pod/delete/patch/apply/shell 等）默认不可逆 → R2。
func Generate(op Operation) Result {
	tool := strings.ToLower(strings.TrimSpace(op.Tool))
	switch tool {
	case "scale_deployment", "scale_statefulset", "scale":
		from, ok := intArg(op.Args, "from_replicas")
		if !ok {
			from, ok = intArg(op.Args, "replicas_from")
		}
		if !ok {
			return Result{Reversible: false, Reason: "缺少 from_replicas/replicas_from，无法生成 scale 逆操作"}
		}
		ns := strArg(op.Args, "namespace")
		name := strArg(op.Args, "name")
		if ns == "" || name == "" {
			return Result{Reversible: false, Reason: "scale 目标缺 namespace/name"}
		}
		return Result{
			Reversible: true,
			Inverse: &Operation{Tool: tool, Args: map[string]any{
				"namespace": ns, "name": name, "replicas": from, "from_replicas": from,
			}},
			Reason: "scale 回退到 from_replicas",
		}
	case "cordon_node":
		name := strArg(op.Args, "name")
		if name == "" {
			return Result{Reversible: false, Reason: "cordon 缺节点名"}
		}
		return Result{Reversible: true, Inverse: &Operation{Tool: "uncordon_node", Args: map[string]any{"name": name}}, Reason: "cordon 逆操作为 uncordon"}
	case "restart_pod":
		return Result{Reversible: false, Reason: "restart_pod 效果不可回滚，必须 R2 人审"}
	case "delete_pod", "delete_deployment", "delete_statefulset":
		return Result{Reversible: false, Reason: "删除类操作无法由确定性规则重建，必须 R2"}
	case "patch", "apply", "create":
		return Result{Reversible: false, Reason: "通用写操作缺少可证明的逆 patch，必须 R2"}
	default:
		return Result{Reversible: false, Reason: "未知工具，无法生成逆操作（fail-closed R2）"}
	}
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if s, ok := args[key].(string); ok {
		return s
	}
	return ""
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

// Validate 校验逆操作与正向操作的互逆性（仅对 scale/cordon 的已知形态做最小断言）。
func Validate(forward, inverse Operation) error {
	if inverse.Tool == "" {
		return fmt.Errorf("逆操作 tool 为空")
	}
	switch strings.ToLower(forward.Tool) {
	case "scale_deployment", "scale_statefulset", "scale":
		if inverse.Tool != strings.ToLower(forward.Tool) {
			return fmt.Errorf("scale 逆操作工具不匹配：%s", inverse.Tool)
		}
		if strArg(forward.Args, "namespace") != strArg(inverse.Args, "namespace") ||
			strArg(forward.Args, "name") != strArg(inverse.Args, "name") {
			return fmt.Errorf("scale 逆操作目标不匹配")
		}
		if _, ok := intArg(inverse.Args, "replicas"); !ok {
			return fmt.Errorf("scale 逆操作缺 replicas")
		}
		return nil
	case "cordon_node":
		if inverse.Tool != "uncordon_node" || strArg(forward.Args, "name") != strArg(inverse.Args, "name") {
			return fmt.Errorf("cordon 逆操作不匹配")
		}
		return nil
	default:
		return fmt.Errorf("未知正向操作 %q，无法校验逆操作", forward.Tool)
	}
}
