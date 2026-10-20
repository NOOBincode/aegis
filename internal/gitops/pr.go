// Package gitops 是 R2 变更通道的纯领域契约（M2 T2.10，ADR-001）。
//
// 它只构造“应该提交什么 PR”的只读/可评审产物，不持有集群凭证、不直接改集群；
// 真正的 GitHub/GitLab API 调用在 adapters 层。白名单、五段模板、patch 上限、
// CR 绑定都在这里确定性校验；任何缺段/越界失败 = 没有 PR（fail-closed）。
package gitops

import (
	"fmt"
	"regexp"
	"strings"
)

const maxPatchBytes = 64 << 10

// Request 是一次 R2 GitOps 变更意图。
type Request struct {
	Repo        string
	CRRef       string
	Description string
	Patch       []byte
	Diagnosis   string // 诊断链摘要（来自 DecisionRecord，不由 Agent 自述）
	BlastRadius string // pods/nodes/namespaces 三元组原值
	RiskLevel   string
	Rollback    string
}

// PR 是待提交的 PR 产物；NoClusterMutation 恒 true，明确本通道不触达集群。
type PR struct {
	Repo              string
	Branch            string
	Title             string
	Body              string
	Patch             []byte
	NoClusterMutation bool
}

var crRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Build 校验并构造 PR；repo 必须在白名单，描述五段必须可渲染，patch ≤64KB。
func Build(req Request, repoWhitelist []string) (PR, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return PR{}, fmt.Errorf("repo 不能为空")
	}
	if !contains(repoWhitelist, req.Repo) {
		return PR{}, fmt.Errorf("repo %q 不在白名单", req.Repo)
	}
	if !crRefPattern.MatchString(req.CRRef) {
		return PR{}, fmt.Errorf("cr_ref 非法")
	}
	if len(req.Patch) == 0 {
		return PR{}, fmt.Errorf("manifest_patch 不能为空")
	}
	if len(req.Patch) > maxPatchBytes {
		return PR{}, fmt.Errorf("manifest_patch 超过 64KB，需拆分")
	}
	if strings.TrimSpace(req.Description) == "" {
		return PR{}, fmt.Errorf("description 不能为空")
	}
	for name, v := range map[string]string{
		"诊断链": req.Diagnosis, "影响面": req.BlastRadius, "变更内容": req.Description,
		"期望级别": req.RiskLevel, "回滚方式": req.Rollback,
	} {
		if strings.TrimSpace(v) == "" {
			return PR{}, fmt.Errorf("PR 描述缺段：%s", name)
		}
	}
	branch := "aegis/" + sanitize(req.CRRef)
	body := strings.Join([]string{
		"## 诊断链\n" + req.Diagnosis,
		"## 影响面\n" + req.BlastRadius,
		"## 变更内容\n" + req.Description,
		"## 期望处置级别\n" + req.RiskLevel,
		"## 回滚方式\n" + req.Rollback,
		"\n> 本 PR 由 aegis R2 GitOps 通道生成；不含集群凭证，合并后由 ArgoCD 既有 sync 接管。",
	}, "\n\n")
	return PR{
		Repo: req.Repo, Branch: branch, Title: "aegis: " + req.CRRef,
		Body: body, Patch: req.Patch, NoClusterMutation: true,
	}, nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "unknown"
	}
	return out
}
