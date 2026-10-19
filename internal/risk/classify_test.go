package risk

import "testing"

func TestReadOnlyIsR0(t *testing.T) {
	for _, tool := range []string{"get_pods", "scan_cluster", "prom_query", "aggregate_logs"} {
		if got := Classify(Input{Tool: tool}); got.Level != LevelR0 || got.RequiresHuman {
			t.Fatalf("%s: %+v", tool, got)
		}
	}
}

func TestScaleStep(t *testing.T) {
	ok := Classify(Input{Tool: "scale_deployment", Verb: "patch", Resource: "deployments", HasInverse: true, DryRunPassed: true, Args: map[string]any{"delta": 1}})
	if ok.Level != LevelR1 || !ok.AutoExecutable || ok.HardDeny {
		t.Fatalf("scale+1 应为 R1 自动：%+v", ok)
	}
	big := Classify(Input{Tool: "scale_deployment", Verb: "patch", Resource: "deployments", HasInverse: true, DryRunPassed: true, Args: map[string]any{"delta": 3}})
	if big.Level != LevelR2 || !big.RequiresHuman {
		t.Fatalf("scale+3 应升 R2：%+v", big)
	}
}

func TestNoInverseEscalates(t *testing.T) {
	got := Classify(Input{Tool: "restart_pod", Verb: "delete", Resource: "pods", DryRunPassed: true, HasInverse: false})
	if got.Level != LevelR2 || got.AutoExecutable {
		t.Fatalf("无逆操作应 R2：%+v", got)
	}
}

func TestHardDenyWins(t *testing.T) {
	for _, in := range []Input{
		{Tool: "delete_namespace", Verb: "delete", Resource: "namespaces"},
		{Tool: "patch", Verb: "patch", Resource: "secrets"},
		{Tool: "restart_pod", Resource: "pods", Namespace: "kube-system", HasInverse: true, DryRunPassed: true},
		{Tool: "shell", Verb: "exec"},
		{Tool: "get_secret", Verb: "get", Resource: "secrets"},
	} {
		got := Classify(in)
		if !got.HardDeny || got.Level != LevelR3 || got.AutoExecutable {
			t.Fatalf("应 R3 硬拒：in=%+v out=%+v", in, got)
		}
	}
}

func TestInjectionAndOwnerEscalate(t *testing.T) {
	base := Input{Tool: "restart_pod", Verb: "delete", Resource: "pods", HasInverse: true, DryRunPassed: true}
	inj := base
	inj.InjectionHit = true
	if got := Classify(inj); got.Level != LevelR2 || got.AutoExecutable {
		t.Fatalf("注入命中应 R2：%+v", got)
	}
	own := base
	own.Owner = "hpa/payments"
	if got := Classify(own); got.Level != LevelR2 || got.AutoExecutable {
		t.Fatalf("owner 非空应 R2：%+v", got)
	}
}

func TestUnknownToolFailClosed(t *testing.T) {
	got := Classify(Input{Tool: "mystery", Verb: "patch"})
	if got.Level != LevelR2 || !got.RequiresHuman {
		t.Fatalf("未知工具应 R2：%+v", got)
	}
}

func TestReadsInProtectedNamespacesAllowed(t *testing.T) {
	got := Classify(Input{Tool: "get_pods", Verb: "list", Resource: "pods", Namespace: "kube-system"})
	if got.Level != LevelR0 || got.HardDeny {
		t.Fatalf("读保护命名空间应 R0：%+v", got)
	}
	rbac := Classify(Input{Tool: "list_clusterroles", Verb: "list", Resource: "clusterroles"})
	if rbac.Level != LevelR0 || rbac.HardDeny {
		t.Fatalf("读 RBAC 应 R0：%+v", rbac)
	}
}
