package scan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// B 层：数据驱动结构缺陷 lint 规则（借鉴 kubeagent policy DSL，纯自研）。
// 规则是数据不是代码——可热修改、可评审、可 eval（抗规则腐烂 R30 主载体）。
//
// 领域纯净说明（铁律 2）：本文件只定义【已解析】的 PolicyRule 结构、强校验与
// Evaluate 纯函数，不做 YAML 解码——YAML 文本 → []PolicyRule 的解码发生在装配边缘
// （service/cmd），domain 因此不依赖 sigs.k8s.io 等基础设施库。

// ruleIDPattern 规则 id 合法字符（加载期强校验，防注入式 id）。
var ruleIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// PolicyRule 一条结构 lint 规则（YAML 条目解码后的形态）。
type PolicyRule struct {
	ID      string       `json:"id" yaml:"id"`
	Match   PolicyMatch  `json:"match" yaml:"match"`
	Assert  PolicyAssert `json:"assert" yaml:"assert"`
	Level   Severity     `json:"level" yaml:"level"` // info|warning|critical
	Message string       `json:"message" yaml:"message"`
}

// PolicyMatch 规则作用范围。labels 暂不支持（校验期拒绝，避免静默忽略）。
type PolicyMatch struct {
	Kind       string   `json:"kind" yaml:"kind"`             // 如 Deployment
	Namespaces []string `json:"namespaces" yaml:"namespaces"` // ["*"]=全部；空=全部
}

// PolicyAssert 断言：恰设 path+op（+values）之一组。relation 暂不支持（校验期拒绝）。
type PolicyAssert struct {
	Path   string   `json:"path" yaml:"path"`     // 白名单叶子路径（见 supportedPaths）
	Op     string   `json:"op" yaml:"op"`         // 封闭操作集
	Values []string `json:"values" yaml:"values"` // op 为 in/notIn/matches/gt... 时的右值
}

// 封闭操作集（故意封闭：表达式语言是第二个要 fuzz/sandbox/version 的面）。
const (
	OpExists     = "exists"
	OpNotExists  = "notExists"
	OpIn         = "in"
	OpNotIn      = "notIn"
	OpMatches    = "matches"
	OpNotMatches = "notMatches"
	OpGt         = "gt"
	OpGte        = "gte"
	OpLt         = "lt"
	OpLte        = "lte"
)

// validOps 封闭操作集合集。
var validOps = map[string]bool{
	OpExists: true, OpNotExists: true, OpIn: true, OpNotIn: true,
	OpMatches: true, OpNotMatches: true, OpGt: true, OpGte: true, OpLt: true, OpLte: true,
}

// supportedPaths 白名单叶子路径（针对工作负载模板容器的数据最小化判定面）。
// 刻意不含 secret/env/data 等敏感面（对齐 kubeagent 的 Secret/ConfigMap-data 排除）。
var supportedPaths = map[string]bool{
	"spec.template.spec.containers[*].resources.limits.memory":    true,
	"spec.template.spec.containers[*].resources.limits.cpu":       true,
	"spec.template.spec.containers[*].securityContext.privileged": true,
	"spec.template.spec.containers[*].readinessProbe":             true,
	"spec.template.spec.containers[*].livenessProbe":              true,
	"spec.template.spec.containers[*].imageTag":                   true,
}

// Validate 加载期 fail-fast 强校验（对齐 kubeagent load.go 精神）。
func (r PolicyRule) Validate() error {
	if !ruleIDPattern.MatchString(r.ID) {
		return fmt.Errorf("policy id 非法（须 ^[A-Za-z0-9._-]+$）：%q", r.ID)
	}
	switch r.Match.Kind {
	case "Deployment":
		// 当前 Snapshot 只采集 Deployment；只允许能真正评估的 kind。
	case "StatefulSet", "DaemonSet", "Job", "CronJob":
		// 刻意拒绝而不是"校验通过但 EvaluatePolicies 静默跳过"：静默不生效的规则
		// 比没有规则更危险（报告显示"规则库已覆盖"实际从未执行）。接入对应采集后再放开。
		return fmt.Errorf("policy %q kind %q 暂不支持（Snapshot 仅采集 Deployment，避免静默不生效）", r.ID, r.Match.Kind)
	case "Secret", "ConfigMap":
		return fmt.Errorf("policy %q 禁用涉密 kind %q（数据最小化）", r.ID, r.Match.Kind)
	default:
		return fmt.Errorf("policy %q 未支持的 kind %q", r.ID, r.Match.Kind)
	}
	if !validOps[r.Assert.Op] {
		return fmt.Errorf("policy %q 非法 op %q（封闭操作集）", r.ID, r.Assert.Op)
	}
	if !supportedPaths[r.Assert.Path] {
		return fmt.Errorf("policy %q 未支持的 assert.path %q", r.ID, r.Assert.Path)
	}
	switch r.Assert.Op {
	case OpIn, OpNotIn, OpMatches, OpNotMatches, OpGt, OpGte, OpLt, OpLte:
		if len(r.Assert.Values) == 0 {
			return fmt.Errorf("policy %q op=%q 需要 values", r.ID, r.Assert.Op)
		}
	}
	// 数值比较 op 的 values 必须在加载期可解析（防一处 quantity 笔误让规则静默失效，R30）。
	switch r.Assert.Op {
	case OpGt, OpGte, OpLt, OpLte:
		for _, v := range r.Assert.Values {
			if !isValidQuantity(v) {
				return fmt.Errorf("policy %q op=%q 的 value %q 不是可解析的 quantity（如 500m/512Mi/2Gi）", r.ID, r.Assert.Op, v)
			}
		}
	}
	if r.Level != SeverityInfo && r.Level != SeverityWarning && r.Level != SeverityCritical {
		return fmt.Errorf("policy %q level 非法（须 info|warning|critical）：%q", r.ID, r.Level)
	}
	if r.Message == "" {
		return fmt.Errorf("policy %q 缺 message", r.ID)
	}
	return nil
}

// ValidateRuleSet 校验一组规则：逐条校验 + 跨条目重复 id 报错（对齐 kubeagent）。
func ValidateRuleSet(rules []PolicyRule) error {
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			return err
		}
		if seen[r.ID] {
			return fmt.Errorf("policy id 重复：%q", r.ID)
		}
		seen[r.ID] = true
	}
	return nil
}

// EvaluatePolicies 对 Snapshot 应用 B 层规则，产出命中 Finding。
// 纯函数（无 IO）；按 Deployment 有界并行扇出（每 goroutine 只写自己的槽位，
// pmap 按索引隔离零共享写），输出经上层统一排序保确定性。
// 未匹配/未覆盖的对象记为 notEvaluated 语义由上层经盲点处理。
func EvaluatePolicies(rules []PolicyRule, s Snapshot, workers int) []Finding {
	per := pmap(workers, s.Deployments, func(d DeploymentInfo) []Finding {
		hits := make([]Finding, 0)
		for _, r := range rules {
			if r.Match.Kind != "Deployment" || !nsMatch(r.Match.Namespaces, d.Namespace) {
				continue
			}
			hits = append(hits, evalOnDeployment(r, d)...)
		}
		return hits
	})
	out := make([]Finding, 0)
	for _, hits := range per {
		out = append(out, hits...)
	}
	return out
}

// evalOnDeployment 对单 Deployment 的每个容器应用一条规则。
func evalOnDeployment(r PolicyRule, d DeploymentInfo) []Finding {
	out := make([]Finding, 0)
	for _, c := range d.Containers {
		if assertViolated(r.Assert, c) {
			obj := ObjRef{Kind: "Deployment", Namespace: d.Namespace, Name: d.Name}
			f := newFinding("policy/"+r.ID, obj, r.Level, r.Message)
			f.Evidence = append(f.Evidence, "容器="+c.Name)
			f.Suggestion = "按结构最佳实践修正工作负载声明（经由 GitOps/审批通道，不直改）"
			out = append(out, f)
		}
	}
	return out
}

// assertViolated 判定容器是否违反断言（true=违反→产出 finding）。
// 防御：需要右值的 op 若 values 为空，视为不违反（避免对未校验规则索引 panic）——
// 正常路径经 ValidateRuleSet 前置拦截，此处兜底导出纯函数被直接调用的情形。
func assertViolated(a PolicyAssert, c WorkloadContainer) bool {
	fv := fieldValue(a.Path, c)
	switch a.Op {
	case OpMatches, OpNotMatches, OpGt, OpGte, OpLt, OpLte:
		if len(a.Values) == 0 {
			return false
		}
	}
	switch a.Op {
	case OpExists:
		return !fv.present
	case OpNotExists:
		return fv.present
	case OpIn:
		return !contains(a.Values, fv.str)
	case OpNotIn:
		return contains(a.Values, fv.str)
	case OpMatches:
		return !globMatch(a.Values[0], fv.str)
	case OpNotMatches:
		return globMatch(a.Values[0], fv.str)
	case OpGt:
		return !fv.present || fv.num <= mustQuantity(a.Values[0])
	case OpGte:
		return !fv.present || fv.num < mustQuantity(a.Values[0])
	case OpLt:
		return !fv.present || fv.num >= mustQuantity(a.Values[0])
	case OpLte:
		return !fv.present || fv.num > mustQuantity(a.Values[0])
	}
	return false
}

// cval 容器字段的统一取值（present=字段是否设置；num=数值；str=字符串）。
type cval struct {
	present bool
	num     int64
	str     string
}

// fieldValue 把白名单 path 映射到容器字段值（类型安全，避免 unstructured）。
func fieldValue(path string, c WorkloadContainer) cval {
	switch path {
	case "spec.template.spec.containers[*].resources.limits.memory":
		return cval{present: c.MemoryLimitBytes > 0, num: c.MemoryLimitBytes}
	case "spec.template.spec.containers[*].resources.limits.cpu":
		return cval{present: c.CPULimitMilli > 0, num: c.CPULimitMilli}
	case "spec.template.spec.containers[*].securityContext.privileged":
		return cval{present: c.Privileged, str: boolStr(c.Privileged)}
	case "spec.template.spec.containers[*].readinessProbe":
		return cval{present: c.HasReadinessProbe, str: boolStr(c.HasReadinessProbe)}
	case "spec.template.spec.containers[*].livenessProbe":
		return cval{present: c.HasLivenessProbe, str: boolStr(c.HasLivenessProbe)}
	case "spec.template.spec.containers[*].imageTag":
		return cval{present: true, str: imageTag(c.Image)}
	}
	return cval{}
}

func nsMatch(patterns []string, ns string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == "*" || p == ns {
			return true
		}
	}
	return false
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// imageTag 取镜像 tag（无 tag 视为 latest，供 :latest 规则判定）。
func imageTag(image string) string {
	if i := strings.LastIndex(image, ":"); i >= 0 && !strings.Contains(image[i:], "/") {
		return image[i+1:]
	}
	return "latest"
}

// globMatch 极简 glob：仅支持首尾 * 的包含匹配（封闭语义，防表达式膨胀）。
func globMatch(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	pre := strings.HasPrefix(pattern, "*")
	suf := strings.HasSuffix(pattern, "*")
	core := strings.Trim(pattern, "*")
	switch {
	case pre && suf:
		return strings.Contains(s, core)
	case pre:
		return strings.HasSuffix(s, core)
	case suf:
		return strings.HasPrefix(s, core)
	default:
		return s == pattern
	}
}

// mustQuantity 解析 K8s quantity 简版（数字含小数 + m/Ki/Mi/Gi/Ti 后缀）为可比数值。
// CPU 用毫核（500m=500），内存用字节（1Gi=1073741824，支持 1.5Gi）。解析失败返回 0。
// domain 自研解析（铁律 2 禁 k8s.io/apimachinery resource.Quantity）。
//
// 单位语义说明：数值比较是无单位的——memory path 按字节、cpu path 按毫核（见 fieldValue），
// 规则 values 须与对应字段同单位（cpu 写毫核如 "2000"/"2000m"等价语义需规则作者注意，
// 纯数字 cpu 值按毫核理解）。加载期 isValidQuantity 已拦截不可解析值。
func mustQuantity(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "m") { // CPU 毫核
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		if err != nil {
			return 0
		}
		return int64(f)
	}
	mult := int64(1)
	num := s
	for _, suf := range []struct {
		s string
		m int64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}} {
		if strings.HasSuffix(s, suf.s) {
			mult = suf.m
			num = strings.TrimSuffix(s, suf.s)
			break
		}
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(mult))
}

// isValidQuantity 判断字符串是否为可解析的 quantity（加载期校验用）。
func isValidQuantity(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasSuffix(s, "m") {
		_, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		return err == nil
	}
	for _, suf := range []string{"Ki", "Mi", "Gi", "Ti"} {
		if strings.HasSuffix(s, suf) {
			_, err := strconv.ParseFloat(strings.TrimSuffix(s, suf), 64)
			return err == nil
		}
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}
