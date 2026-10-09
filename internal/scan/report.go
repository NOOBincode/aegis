package scan

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// 报告渲染（design.md §3.4，借鉴 kubeagent 输出三件套，纯自研）。
// 输出形态：版本化 JSON（scan-v1）+ SARIF 2.1.0；HTML 单文件零 JS 为可选增强（未实现）。
// 脱敏（I4/§4.2.2）：Finding 字段已在引擎层截断单行化；Snapshot 采集侧已数据最小化，
// 报告不含集群身份（context 名/apiserver 地址/kubeconfig 路径）与 secret/env/镜像仓库路径。

// schemaScanV1 是版本化 JSON 报告的 schema 标识（便于回放/回归比对演进）。
const schemaScanV1 = "scan-v1"

// reportJSON 版本化 JSON 报告结构（scan-v1）。
type reportJSON struct {
	Schema       string    `json:"schema"`
	GeneratedAt  string    `json:"generated_at"`
	Summary      summary   `json:"summary"`
	Findings     []Finding `json:"findings"`
	PartialReads []string  `json:"partial_reads,omitempty"`
	ExecutionOK  bool      `json:"execution_successful"`
}

type summary struct {
	TotalFindings   int `json:"total_findings"`
	Critical        int `json:"critical"`
	Warning         int `json:"warning"`
	Info            int `json:"info"`
	ScannedPods     int `json:"scanned_pods"`
	ResolvedByRules int `json:"resolved_by_rules"` // 规则层解决率分子（M3 eval）
}

// WriteJSON 渲染版本化 JSON 报告（scan-v1）。盲点非空时 execution_successful=false
// （对齐 kubeagent：有未豁免盲点不上传"干净"结果）。
func WriteJSON(w io.Writer, r Result) error {
	s := summary{ScannedPods: r.ScannedPods, ResolvedByRules: r.ResolvedByRules, TotalFindings: len(r.Findings)}
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityCritical:
			s.Critical++
		case SeverityWarning:
			s.Warning++
		case SeverityInfo:
			s.Info++
		}
	}
	doc := reportJSON{
		Schema:       schemaScanV1,
		GeneratedAt:  r.GeneratedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Summary:      s,
		Findings:     r.Findings,
		PartialReads: r.PartialReads,
		ExecutionOK:  len(r.PartialReads) == 0,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// WriteText 渲染人类可读文本报告（CLI 默认输出）。含摘要、逐条 finding 与盲点。
func WriteText(w io.Writer, r Result) error {
	var err error
	fprintf := func(format string, args ...any) {
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(w, format, args...)
	}
	crit, warn, info := 0, 0, 0
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityCritical:
			crit++
		case SeverityWarning:
			warn++
		case SeverityInfo:
			info++
		}
	}
	fprintf("aegis 规则巡检（Tier-0 确定性，离线零 LLM）  %s\n", r.GeneratedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	fprintf("扫描 Pod %d 个 · 命中 %d 条（严重 %d / 警告 %d / 提示 %d）· 规则结案 %d 条\n\n",
		r.ScannedPods, len(r.Findings), crit, warn, info, r.ResolvedByRules)
	for _, f := range r.Findings {
		fprintf("[%s] %s  %s\n", severityTag(f.Severity), f.Object.String(), f.RootCause)
		fprintf("    规则=%s 置信度=%s\n", f.RuleID, f.Confidence)
		for _, e := range f.Evidence {
			fprintf("    证据: %s\n", e)
		}
		if f.Suggestion != "" {
			fprintf("    建议: %s\n", f.Suggestion)
		}
	}
	if len(r.PartialReads) > 0 {
		fprintf("\n盲点（采集受限，结果可能不全）: %s\n", joinComma(r.PartialReads))
	}
	return err
}

func severityTag(s Severity) string {
	switch s {
	case SeverityCritical:
		return "严重"
	case SeverityWarning:
		return "警告"
	default:
		return "提示"
	}
}

func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// ---- SARIF 2.1.0 ----

const sarifVersion = "2.1.0"

// SARIF 结构（最小子集，可上传 GitHub code scanning）。
type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}
type sarifRun struct {
	Tool        sarifTool         `json:"tool"`
	Results     []sarifResult     `json:"results"`
	Invocations []sarifInvocation `json:"invocations"`
}
type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}
type sarifDriver struct {
	Name  string      `json:"name"`
	Rules []sarifRule `json:"rules"`
}
type sarifRule struct {
	ID               string       `json:"id"`
	ShortDescription sarifMessage `json:"shortDescription"`
}
type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"` // error|warning|note
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations"`
}
type sarifMessage struct {
	Text string `json:"text"`
}
type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}
type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
}
type sarifArtifact struct {
	URI string `json:"uri"` // k8s://<ns>/<Kind>/<name>
}
type sarifInvocation struct {
	ExecutionSuccessful bool `json:"executionSuccessful"`
}

// WriteSARIF 渲染 SARIF 2.1.0。finding 是集群对象无文件可指，合成 k8s:// URI、不带 region；
// 只声明实际触发的 rule；有盲点时 executionSuccessful=false（对齐 kubeagent）。
func WriteSARIF(w io.Writer, r Result) error {
	rules := map[string]bool{}
	results := make([]sarifResult, 0, len(r.Findings))
	for _, f := range r.Findings {
		rules[f.RuleID] = true
		results = append(results, sarifResult{
			RuleID:  f.RuleID,
			Level:   severityToSARIF(f.Severity),
			Message: sarifMessage{Text: f.RootCause},
			Locations: []sarifLocation{{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifact{URI: k8sURI(f.Object)},
				},
			}},
		})
	}
	// 只声明实际触发的 rule，且按 id 排序——同一扫描两次产出字节一致（确定性，供审计/回归比对）。
	ids := make([]string, 0, len(rules))
	for id := range rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ruleList := make([]sarifRule, 0, len(ids))
	for _, id := range ids {
		ruleList = append(ruleList, sarifRule{ID: id, ShortDescription: sarifMessage{Text: id}})
	}
	log := sarifLog{
		Version: sarifVersion,
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []sarifRun{{
			Tool:        sarifTool{Driver: sarifDriver{Name: "aegis-scan", Rules: ruleList}},
			Results:     results,
			Invocations: []sarifInvocation{{ExecutionSuccessful: len(r.PartialReads) == 0}},
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

// k8sURI 合成集群对象的 SARIF artifact URI（无文件路径可指）。
func k8sURI(o ObjRef) string {
	if o.Namespace == "" {
		return fmt.Sprintf("k8s://%s/%s", o.Kind, o.Name)
	}
	return fmt.Sprintf("k8s://%s/%s/%s", o.Namespace, o.Kind, o.Name)
}

// severityToSARIF 映射严重度到 SARIF level（Critical→error / Warning→warning / Info→note）。
func severityToSARIF(s Severity) string {
	switch s {
	case SeverityCritical:
		return "error"
	case SeverityWarning:
		return "warning"
	default:
		return "note"
	}
}
