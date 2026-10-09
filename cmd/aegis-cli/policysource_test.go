package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aegis-dev/aegis/internal/scan"
)

// TestDefaultPolicyPackDecodes 验证嵌入默认规则包可解码且通过强校验（P2.3 兜底）。
// 抗规则腐烂（R17）：一个 YAML 拼写错误不应让全部 B 层规则在运行时静默退化为盲点——
// CI 必须能抓到。本测试断言默认包可解码、校验通过、含预期规则 id。
func TestDefaultPolicyPackDecodes(t *testing.T) {
	rules, err := decodePolicyFile(scan.DefaultReliabilityYAML)
	if err != nil {
		t.Fatalf("默认规则包解码失败：%v", err)
	}
	if err := scan.ValidateRuleSet(rules); err != nil {
		t.Fatalf("默认规则包校验失败：%v", err)
	}
	if len(rules) == 0 {
		t.Fatal("默认规则包不应为空")
	}
	// 含预期规则 id（与 reliability.yaml 对账）。
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

// TestFilePolicySourceEmptyDirFallsBack 验证空规则目录回退嵌入默认（避免误配导致空规则裸奔）。
func TestFilePolicySourceEmptyDirFallsBack(t *testing.T) {
	rules, err := FilePolicySource{Dir: t.TempDir()}.Load()
	if err != nil {
		t.Fatalf("空目录应回退默认而非报错：%v", err)
	}
	if len(rules) == 0 {
		t.Error("空目录应回退嵌入默认规则")
	}
}

// TestFilePolicySourceBadYAML 验证坏规则文件加载报错（供 service fail-safe 回退）。
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
	// 解码成功但含非法 id，应由 service 的 ValidateRuleSet 拦截并回退。
	if verr := scan.ValidateRuleSet(rules); verr == nil {
		t.Error("非法 id 应被 ValidateRuleSet 拦截")
	}
}
