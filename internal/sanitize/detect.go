package sanitize

import (
	"regexp"
	"strings"
)

// InjectionHit 是 I4 注入模式命中结果。只返回模式 ID/计数，不返回命中原文，
// 避免把恶意文本再次写入日志/DecisionRecord（M2 T2.7；LLM 动作升级由闸门裁决）。
type InjectionHit struct {
	Matched  bool
	Patterns []string
}

var injectionPatterns = []struct {
	id string
	re *regexp.Regexp
}{
	{"ignore-instructions", regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|prior|above)\s+instructions?`)},
	{"disregard-instructions", regexp.MustCompile(`(?i)disregard\s+(all\s+)?(previous|prior)\s+instructions?`)},
	{"role-switch", regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)?\s*(admin|root|system|developer|assistant)`)},
	{"system-prompt", regexp.MustCompile(`(?i)system\s+(prompt|message)\s*[:：]`)},
	{"delete-namespace", regexp.MustCompile(`(?i)\bdelete\s+(the\s+)?namespace\s+[a-z0-9][a-z0-9-]{0,62}`)},
	{"kubectl-delete", regexp.MustCompile(`(?i)\bkubectl\s+delete\b`)},
	{"pipe-to-shell", regexp.MustCompile(`(?i)\b(curl|wget)\b[^\n|]{0,200}\|\s*(sh|bash)\b`)},
	{"rm-rf-root", regexp.MustCompile(`(?i)\brm\s+-rf\s+/(\s|$)`)},
	{"fence-break", regexp.MustCompile(`(?i)</?untrusted_cluster_data>|</?trusted>`)},
}

// DetectInjection 检测不可信数据中的指令型/围栏逃逸模式。
func DetectInjection(s string) InjectionHit {
	if strings.TrimSpace(s) == "" {
		return InjectionHit{}
	}
	h := InjectionHit{}
	for _, p := range injectionPatterns {
		if p.re.MatchString(s) {
			h.Matched = true
			h.Patterns = append(h.Patterns, p.id)
		}
	}
	return h
}
