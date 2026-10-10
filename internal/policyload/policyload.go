// Package policyload 是 B 层规则（YAML）的加载器：把 YAML 文件/嵌入默认解码为
// []scan.PolicyRule，实现 scan.PolicySource 端口。供 cmd/aegis-cli 与 mcp-servers/scan 共用。
//
// 架构位置（铁律 2）：YAML 解码（sigs.k8s.io/yaml）属于基础设施/装配边缘，不在
// internal/scan domain——domain 只面向已解析的 PolicyRule，本包承担解码这一 IO/解析职责。
package policyload

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
// 解码用 UnmarshalStrict 强校验；失败返回 error 由 scan.Service 做 fail-safe 回退。
type FilePolicySource struct {
	// Dir 规则目录（读其中所有 .yaml/.yml）；空 = 用嵌入默认规则。
	Dir string
}

// Load 读取并解码当前规则集（每次调用重读磁盘 → 热修改生效）。
func (f FilePolicySource) Load() ([]scan.PolicyRule, error) {
	if f.Dir == "" {
		return DefaultRules()
	}
	files, err := policyFiles(f.Dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		// 目录存在但无规则文件：回退嵌入默认（避免误配导致空规则裸奔）。
		return DefaultRules()
	}
	var all []scan.PolicyRule
	root := filepath.Clean(f.Dir)
	for _, file := range files {
		// 路径约束：仅读规则目录内的文件（防路径穿越）。file 来自 glob(dir/*.yaml)，
		// 此处再 Clean + 前缀校验做纵深防御。
		clean := filepath.Clean(file)
		if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return nil, fmt.Errorf("规则文件越出规则目录：%s", file)
		}
		//nolint:gosec // G304：路径已约束在规则目录内（glob 结果 + Clean 前缀校验），非任意输入。
		data, err := os.ReadFile(clean)
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

// DefaultRules 返回嵌入的默认规则包（解码 scan.DefaultReliabilityYAML）。
// 结果缓存（默认包不随运行变化）；解码失败是编程错误（编译期内嵌），返回 error。
func DefaultRules() ([]scan.PolicyRule, error) {
	return defaultRules()
}

var (
	defaultOnce  sync.Once
	defaultCache []scan.PolicyRule
	defaultErr   error
)

func defaultRules() ([]scan.PolicyRule, error) {
	defaultOnce.Do(func() {
		defaultCache, defaultErr = decodePolicyFile(scan.DefaultReliabilityYAML)
	})
	return defaultCache, defaultErr
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
