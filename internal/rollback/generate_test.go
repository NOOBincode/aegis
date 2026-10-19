package rollback

import "testing"

func TestScaleInverse(t *testing.T) {
	op := Operation{Tool: "scale_deployment", Args: map[string]any{"namespace": "shop", "name": "payments", "replicas": 4, "from_replicas": 2}}
	got := Generate(op)
	if !got.Reversible || got.Inverse == nil {
		t.Fatalf("%+v", got)
	}
	if got.Inverse.Args["replicas"] != 2 {
		t.Fatalf("inverse=%+v", got.Inverse)
	}
	if err := Validate(op, *got.Inverse); err != nil {
		t.Fatal(err)
	}
}

func TestScaleNoFromIsIrreversible(t *testing.T) {
	got := Generate(Operation{Tool: "scale_deployment", Args: map[string]any{"namespace": "shop", "name": "payments", "replicas": 4}})
	if got.Reversible || got.Inverse != nil {
		t.Fatalf("缺 from 应不可逆：%+v", got)
	}
}

func TestCordonInverse(t *testing.T) {
	op := Operation{Tool: "cordon_node", Args: map[string]any{"name": "worker-1"}}
	got := Generate(op)
	if !got.Reversible || got.Inverse == nil || got.Inverse.Tool != "uncordon_node" {
		t.Fatalf("%+v", got)
	}
	if err := Validate(op, *got.Inverse); err != nil {
		t.Fatal(err)
	}
}

func TestIrreversibleOperations(t *testing.T) {
	for _, tool := range []string{"restart_pod", "delete_pod", "delete_deployment", "patch", "apply", "shell"} {
		got := Generate(Operation{Tool: tool, Args: map[string]any{"namespace": "shop", "name": "x"}})
		if got.Reversible || got.Inverse != nil {
			t.Fatalf("%s 应不可逆：%+v", tool, got)
		}
	}
}

func TestValidateMismatch(t *testing.T) {
	if err := Validate(Operation{Tool: "scale_deployment", Args: map[string]any{"namespace": "shop", "name": "a"}}, Operation{Tool: "uncordon_node", Args: map[string]any{"name": "a"}}); err == nil {
		t.Fatal("工具不匹配应报错")
	}
}
