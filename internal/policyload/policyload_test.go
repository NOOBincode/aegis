package policyload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aegis-dev/aegis/internal/scan"
)

// TestDefaultRulesDecode 验证嵌入默认规则包可解码且通过强校验（抗 R17 静默退化）。
func TestDefaultRulesDecode(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatalf("默认规则包解码失败：%v", err)
	}
	if err := scan.ValidateRuleSet(rules); err != nil {
		t.Fatalf("默认规则包校验失败：%v", err)
	}
	if len(rules) == 0 {
		t.Fatal("默认规则包不应为空")
	}
	want := []string{
		"reliability.no-memory-limit",
		"reliability.no-readiness-probe",
		"reliability.no-liveness-probe",
		"reliability.image-latest-tag",
		"security.privileged-container",
	}
	got := map[string]bool{}
	for _, r := range rules {
		got[r.ID] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("默认包缺规则 %q", id)
		}
	}
}

// TestFilePolicySourceEmptyDirFallsBack 空规则目录回退嵌入默认（避免误配空规则裸奔）。
func TestFilePolicySourceEmptyDirFallsBack(t *testing.T) {
	rules, err := FilePolicySource{Dir: t.TempDir()}.Load()
	if err != nil {
		t.Fatalf("空目录应回退默认而非报错：%v", err)
	}
	if len(rules) == 0 {
		t.Error("空目录应回退嵌入默认规则")
	}
}

// TestFilePolicySourceLoadsCustomDir 从目录加载自定义规则（热修改路径）。
func TestFilePolicySourceLoadsCustomDir(t *testing.T) {
	dir := t.TempDir()
	custom := "rules:\n  - id: custom.no-cpu-limit\n    match: {kind: Deployment}\n    assert: {path: \"spec.template.spec.containers[*].resources.limits.cpu\", op: exists}\n    level: info\n    message: \"no cpu limit\"\n"
	if err := os.WriteFile(filepath.Join(dir, "custom.yaml"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := FilePolicySource{Dir: dir}.Load()
	if err != nil {
		t.Fatalf("加载自定义目录：%v", err)
	}
	if err := scan.ValidateRuleSet(rules); err != nil {
		t.Fatalf("自定义规则校验：%v", err)
	}
	if len(rules) != 1 || rules[0].ID != "custom.no-cpu-limit" {
		t.Errorf("应加载自定义规则：%+v", rules)
	}
}

// TestFilePolicySourceBadYAML 坏规则文件加载报错（供 service fail-safe 回退）。
func TestFilePolicySourceBadYAML(t *testing.T) {
	dir := t.TempDir()
	bad := "rules:\n  - id: \"bad id with space\"\n    match: {kind: Deployment}\n"
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := FilePolicySource{Dir: dir}.Load()
	if err != nil {
		t.Fatalf("解码本身成功（校验在 service 层）：%v", err)
	}
	// 解码成功但含非法 id，应由 scan.ValidateRuleSet 拦截并回退。
	if verr := scan.ValidateRuleSet(rules); verr == nil {
		t.Error("非法 id 应被 ValidateRuleSet 拦截")
	}
}

// TestFilePolicySourceStrictField 未知字段被 UnmarshalStrict 拒绝（拼写错误即报错）。
func TestFilePolicySourceStrictField(t *testing.T) {
	dir := t.TempDir()
	typo := "rules:\n  - id: x.y\n    match: {kind: Deployment}\n    assert: {path: \"spec.template.spec.containers[*].resources.limits.memory\", op: exists}\n    level: warning\n    message: m\n    unknow_field: oops\n"
	if err := os.WriteFile(filepath.Join(dir, "typo.yaml"), []byte(typo), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (FilePolicySource{Dir: dir}).Load(); err == nil {
		t.Error("未知字段应被 UnmarshalStrict 拒绝")
	}
}
