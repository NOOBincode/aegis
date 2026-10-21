// Package main 是 aegis MCP 工具进程：gitops（R2 GitOps PR 通道，M2 T2.10）。
//
// 边界：只构造并提交 PR，不持有集群写凭证、不直接改集群；分支/文件/PR 都经
// GitHub 兼容 API adapter。白名单/五段模板/patch 上限由 internal/gitops 纯领域校验。
package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/aegis-dev/aegis/internal/adapters/git"
	"github.com/aegis-dev/aegis/internal/gitops"
	"github.com/aegis-dev/aegis/internal/mcpserver"
)

const (
	serverName    = "gitops"
	serverVersion = "0.1.0"
	listenAddr    = ":8087"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	reg := mcpserver.NewRegistry()
	reg.MustRegister(openChangePRTool(logger))
	srv := mcpserver.NewServer(serverName, serverVersion, reg)
	logger.Info("gitops 启动", "tools", reg.Len(), "addr", listenAddr)
	if err := srv.ServeHTTP(listenAddr); err != nil {
		logger.Error("gitops serve 失败", "error", err)
		os.Exit(1)
	}
}

func openChangePRTool(logger *slog.Logger) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "open_change_pr",
		Description: "把 R2 变更 patch 以 PR 形式提交到白名单 GitOps 仓库；不修改集群",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"repo", "cr_ref", "description", "manifest_patch", "diagnosis", "blast_radius", "risk_level", "rollback", "file_path"},
			"properties": map[string]any{
				"repo":           map[string]any{"type": "string", "minLength": 1},
				"cr_ref":         map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"description":    map[string]any{"type": "string", "minLength": 1},
				"manifest_patch": map[string]any{"type": "string", "minLength": 1},
				"diagnosis":      map[string]any{"type": "string", "minLength": 1},
				"blast_radius":   map[string]any{"type": "string", "minLength": 1},
				"risk_level":     map[string]any{"type": "string", "enum": []string{"R0", "R1", "R2"}},
				"rollback":       map[string]any{"type": "string", "minLength": 1},
				"file_path":      map[string]any{"type": "string", "minLength": 1},
			},
		},
		Metadata: mcpserver.ToolMetadata{
			RiskHint: mcpserver.RiskR2, Idempotent: false, Reversible: true, TimeoutMs: 30000,
		},
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			repo := str(args, "repo")
			whitelist := splitCSV(os.Getenv("AEGIS_GIT_REPO_WHITELIST"))
			if len(whitelist) == 0 {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "AEGIS_GIT_REPO_WHITELIST 未配置")}
			}
			token := os.Getenv("AEGIS_GIT_TOKEN")
			if token == "" {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "AEGIS_GIT_TOKEN 未配置")}
			}
			owner, name, ok := splitRepo(repo)
			if !ok {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "repo 必须为 owner/name")}
			}
			pr, err := gitops.Build(gitops.Request{
				Repo: repo, CRRef: str(args, "cr_ref"), Description: str(args, "description"),
				Patch: []byte(str(args, "manifest_patch")), Diagnosis: str(args, "diagnosis"),
				BlastRadius: str(args, "blast_radius"), RiskLevel: str(args, "risk_level"), Rollback: str(args, "rollback"),
			}, whitelist)
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "PR 契约校验失败: %v", err)}
			}
			baseURL := firstNonEmpty(os.Getenv("AEGIS_GIT_BASE_URL"), "https://api.github.com")
			client, err := git.New(git.Config{BaseURL: baseURL, Token: token, Owner: owner, Repo: name})
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "git 配置: %v", err)}
			}
			baseBranch := firstNonEmpty(os.Getenv("AEGIS_GIT_BASE_BRANCH"), "main")
			if err := client.EnsureBranch(ctx, pr.Branch, baseBranch); err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, true, "创建分支失败: %v", err)}
			}
			if err := client.PutFile(ctx, pr.Branch, str(args, "file_path"), pr.Patch, "aegis: "+pr.Title); err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, true, "提交文件失败: %v", err)}
			}
			url, err := client.CreatePullRequest(ctx, git.PullRequest{Title: pr.Title, Head: pr.Branch, Base: baseBranch, Body: pr.Body})
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, false, "创建 PR 失败: %v", err)}
			}
			logger.Info("gitops.open_change_pr", "repo", repo, "cr", pr.Branch)
			return &mcpserver.Result{Data: map[string]any{
				"pr_url": url, "branch": pr.Branch, "repo": repo, "no_cluster_mutation": true,
			}}
		},
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func splitCSV(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func splitRepo(v string) (string, string, bool) {
	parts := strings.SplitN(v, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
