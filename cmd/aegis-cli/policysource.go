package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"sigs.k8s.io/yaml"

	"github.com/aegis-dev/aegis/internal/scan"
)

// policyFile 是规则 YAML 文件的顶层结构（rules 数组）。
type policyFile struct {
	Rules []scan.PolicyRule `json:"rules"`
}

// FilePolicySource 实现 scan.PolicySource：从磁盘目录加载 B 层规则（热修改——
// 每次 Load 重读磁盘），目录为空/未指定时用嵌入默认规则（气隙开箱可用）。
//
// YAML 解码在装配边缘（本文件）完成，internal/scan domain 保持纯净（不依赖 YAML 库）。
// 解码用 UnmarshalStrict 强校验；失败返回 error 由 service 做 fail-safe 回退。
type FilePolicySource struct {
	// Dir 规则目录（读其中所有 .yaml/.yml）；空 = 用嵌入默认规则。
	Dir string
}

// Load 读取并解码当前规则集（每次调用重读磁盘 → 热修改生效）。
func (f FilePolicySource) Load() ([]scan.PolicyRule, error) {
	if f.Dir == "" {
		return decodePolicyFile(scan.DefaultReliabilityYAML)
	}
	files, err := policyFiles(f.Dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		// 目录存在但无规则文件：回退嵌入默认（避免误配导致空规则裸奔）。
		return decodePolicyFile(scan.DefaultReliabilityYAML)
	}
	var all []scan.PolicyRule
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("读规则文件 %s: %w", file, err)
		}
		rules, err := decodePolicyFile(data)
		if err != nil {
			return nil, fmt.Errorf("解析规则文件 %s: %w", file, err)
		}
		all = append(all, rules...)
	}
	return all, nil
}

// policyFiles 列出目录下全部 YAML 规则文件（确定性排序）。
func policyFiles(dir string) ([]string, error) {
	var out []string
	for _, ext := range []string{"*.yaml", "*.yml"} {
		fs, err := filepath.Glob(filepath.Join(dir, ext))
		if err != nil {
			return nil, fmt.Errorf("扫描规则目录 %s: %w", dir, err)
		}
		out = append(out, fs...)
	}
	sort.Strings(out)
	return out, nil
}

// decodePolicyFile 用 UnmarshalStrict 解码单个规则文件（字段拼写错误即报错）。
func decodePolicyFile(data []byte) ([]scan.PolicyRule, error) {
	var pf policyFile
	if err := yaml.UnmarshalStrict(data, &pf); err != nil {
		return nil, err
	}
	return pf.Rules, nil
}
