package main

import (
	"context"
	"log/slog"

	"k8s.io/client-go/dynamic"

	"github.com/aegis-dev/aegis/internal/gatekeeper"
	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/policy"
	"github.com/aegis-dev/aegis/internal/risk"
)

// newGatekeeperMCPServer 暴露 request_change：Agent 只提交“写意图”，由 gatekeeper
// 判定并落 ChangeRequest；不暴露任何直接写集群的工具（M2 唯一放行点方向）。
func newGatekeeperMCPServer(dc dynamic.Interface, logger *slog.Logger) *mcpserver.Server {
	reg := mcpserver.NewRegistry()
	svc := &gatekeeper.ChangeRequestService{Client: dc}
	reg.MustRegister(mcpserver.Tool{
		Name:        "request_change",
		Description: "提交一次写变更意图给 gatekeeper；返回判定与 ChangeRequest 名，不直接执行集群写",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"session_ref", "idempotency_key", "intent", "source", "requested_by", "tool", "verb", "resource", "namespace", "args"},
			"properties": map[string]any{
				"session_ref":     map[string]any{"type": "string", "minLength": 1},
				"idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"intent":          map[string]any{"type": "string", "minLength": 1},
				"source":          map[string]any{"type": "string", "enum": []string{"agent", "human"}},
				"requested_by":    map[string]any{"type": "string", "minLength": 1},
				"tool":            map[string]any{"type": "string", "minLength": 1},
				"verb":            map[string]any{"type": "string", "minLength": 1},
				"resource":        map[string]any{"type": "string", "minLength": 1},
				"namespace":       map[string]any{"type": "string", "minLength": 1},
				"args":            map[string]any{"type": "object", "additionalProperties": true},
				"dry_run_passed":  map[string]any{"type": "boolean", "default": false},
			},
		},
		Metadata: mcpserver.ToolMetadata{
			RiskHint: mcpserver.RiskR1, Idempotent: true, Reversible: true,
			EstBlastRadius: mcpserver.BlastRadius{}, TimeoutMs: 10000,
		},
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			opArgs, _ := args["args"].(map[string]any)
			dry, _ := args["dry_run_passed"].(bool)
			req := gatekeeper.SubmitRequest{
				SessionRef: str(args, "session_ref"), IdempotencyKey: str(args, "idempotency_key"),
				Intent: str(args, "intent"), Source: str(args, "source"), RequestedBy: str(args, "requested_by"),
				Policy: policy.Policy{},
				Operations: []gatekeeper.SubmitOperation{{
					Tool: str(args, "tool"), Verb: str(args, "verb"), Resource: str(args, "resource"),
					Namespace: str(args, "namespace"), Args: opArgs, DryRunPassed: dry,
					BlastRadius: risk.BlastRadius{},
				}},
			}
			res, err := svc.Submit(ctx, req)
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, false, "提交失败：%v", err)}
			}
			data := map[string]any{
				"cr_name": res.Name, "created": res.Created,
				"allow": res.Decision.Allow, "requires_approval": res.Decision.RequiresApproval,
				"reject": res.Decision.Reject, "reasons": res.Decision.Reasons,
			}
			logger.Info("gatekeeper.request_change", "cr", res.Name, "created", res.Created, "allow", res.Decision.Allow, "reject", res.Decision.Reject)
			return &mcpserver.Result{Data: data}
		},
	})
	return mcpserver.NewServer("gatekeeper", "0.1.0", reg)
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	if s == "" {
		return ""
	}
	return s
}
