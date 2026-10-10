package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/scan"
)

// fakeScanner 注入构造结果 + 捕获调用参数（验证 handler 正确传递 opts）。
type fakeScanner struct {
	res     scan.Result
	err     error
	gotOpts scan.CollectOptions
	called  int
}

func (f *fakeScanner) Scan(_ context.Context, opts scan.CollectOptions) (scan.Result, error) {
	f.called++
	f.gotOpts = opts
	return f.res, f.err
}

func sampleScanResult() scan.Result {
	return scan.Result{
		GeneratedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		ScannedPods: 3,
		Findings: []scan.Finding{
			{ID: "OOM_KILLED/Pod/shop/p0", RuleID: "OOM_KILLED", Severity: scan.SeverityCritical,
				Confidence: scan.ConfidenceHigh, Object: scan.ObjRef{Kind: "Pod", Namespace: "shop", Name: "p0"},
				RootCause: "容器 c OOMKilled"},
			{ID: "policy/x/Deployment/shop/d0", RuleID: "policy/reliability.no-memory-limit", Severity: scan.SeverityWarning,
				Confidence: scan.ConfidenceHigh, Object: scan.ObjRef{Kind: "Deployment", Namespace: "shop", Name: "d0"},
				RootCause: "未设 memory limit"},
		},
		ResolvedByRules: 2,
	}
}

// ---- 单测：参数解析与传递 ----

func TestScanClusterHandlerArgParsing(t *testing.T) {
	fs := &fakeScanner{res: sampleScanResult()}
	h := scanClusterHandler(fs)

	res := h(context.Background(), map[string]any{
		"namespace": "shop", "format": "json", "since": float64(3600),
	})
	if res.Err != nil {
		t.Fatalf("正常调用不应报错：%v", res.Err)
	}
	if fs.gotOpts.Namespace != "shop" {
		t.Errorf("namespace 传递错误：%q", fs.gotOpts.Namespace)
	}
	if fs.gotOpts.SinceSeconds != 3600 {
		t.Errorf("since 传递错误：%d", fs.gotOpts.SinceSeconds)
	}

	// 缺省参数：namespace 空、since 0。
	fs2 := &fakeScanner{res: sampleScanResult()}
	h2 := scanClusterHandler(fs2)
	if res := h2(context.Background(), map[string]any{}); res.Err != nil {
		t.Fatalf("空参数应正常（默认全集群）：%v", res.Err)
	}
	if fs2.gotOpts.Namespace != "" || fs2.gotOpts.SinceSeconds != 0 {
		t.Errorf("缺省参数应为空/0：%+v", fs2.gotOpts)
	}
}

// ---- 单测：结果结构 ----

func TestScanClusterResultData(t *testing.T) {
	fs := &fakeScanner{res: sampleScanResult()}
	res := scanClusterHandler(fs)(context.Background(), map[string]any{})
	if res.Err != nil {
		t.Fatalf("不应报错：%v", res.Err)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 应为 map，got %T", res.Data)
	}
	sum, ok := data["summary"].(map[string]any)
	if !ok {
		t.Fatalf("缺 summary：%v", data)
	}
	if sum["total_findings"] != 2 || sum["critical"] != 1 || sum["warning"] != 1 {
		t.Errorf("summary 统计错误：%v", sum)
	}
	if sum["resolved_by_rules"] != 2 {
		t.Errorf("resolved_by_rules 错误：%v", sum["resolved_by_rules"])
	}
	findings, ok := data["findings"].([]scan.Finding)
	if !ok || len(findings) != 2 {
		t.Errorf("findings 结构错误：%v", data["findings"])
	}
	if data["schema"] != "scan-v1" {
		t.Errorf("schema 应为 scan-v1：%v", data["schema"])
	}
}

// format=text 附 report_text 字段。
func TestScanClusterTextFormat(t *testing.T) {
	fs := &fakeScanner{res: sampleScanResult()}
	res := scanClusterHandler(fs)(context.Background(), map[string]any{"format": "text"})
	data := res.Data.(map[string]any)
	rt, ok := data["report_text"].(string)
	if !ok || !strings.Contains(rt, "OOMKilled") {
		t.Errorf("format=text 应附 report_text 且含根因：%v", data["report_text"])
	}
}

// format=sarif 附 report_sarif 字段（SARIF 2.1.0 + k8s:// URI）。
func TestScanClusterSARIFFormat(t *testing.T) {
	fs := &fakeScanner{res: sampleScanResult()}
	res := scanClusterHandler(fs)(context.Background(), map[string]any{"format": "sarif"})
	if res.Err != nil {
		t.Fatalf("sarif 不应报错：%v", res.Err)
	}
	data := res.Data.(map[string]any)
	rs, ok := data["report_sarif"].(string)
	if !ok {
		t.Fatalf("format=sarif 应附 report_sarif：%v", data)
	}
	if !strings.Contains(rs, `"version": "2.1.0"`) || !strings.Contains(rs, "OOM_KILLED") || !strings.Contains(rs, "k8s://shop/Pod/p0") {
		t.Errorf("report_sarif 应为合法 SARIF 且含 finding：%s", rs)
	}
}

// since 负数 → schema 错误（不触达 Scanner）。
func TestScanClusterNegativeSince(t *testing.T) {
	fs := &fakeScanner{res: sampleScanResult()}
	res := scanClusterHandler(fs)(context.Background(), map[string]any{"since": float64(-5)})
	if res.Err == nil || res.Err.Class != mcpserver.ErrClassSchema {
		t.Errorf("since 负数应 schema 错误：%+v", res.Err)
	}
	if fs.called != 0 {
		t.Error("since 非法不应触达 Scanner")
	}
}

// ---- 单测：错误协议 ----

func TestScanClusterErrorProtocol(t *testing.T) {
	t.Run("非法format", func(t *testing.T) {
		fs := &fakeScanner{}
		res := scanClusterHandler(fs)(context.Background(), map[string]any{"format": "xml"})
		if res.Err == nil || res.Err.Class != mcpserver.ErrClassSchema {
			t.Errorf("非法 format 应 schema 错误：%+v", res.Err)
		}
		if fs.called != 0 {
			t.Error("format 非法不应触达 Scanner")
		}
	})
	t.Run("scanner错误→downstream可重试", func(t *testing.T) {
		fs := &fakeScanner{err: errors.New("apiserver 不可达")}
		res := scanClusterHandler(fs)(context.Background(), map[string]any{})
		if res.Err == nil || res.Err.Class != mcpserver.ErrClassDownstream || !res.Err.Retryable {
			t.Errorf("scanner 错误应 downstream+retryable：%+v", res.Err)
		}
	})
	t.Run("scanner未装配", func(t *testing.T) {
		res := scanClusterHandler(nil)(context.Background(), map[string]any{})
		if res.Err == nil || res.Err.Class != mcpserver.ErrClassInternal {
			t.Errorf("scanner nil 应 internal 错误：%+v", res.Err)
		}
	})
}

// ---- 白盒：注册元数据 + schema 契约 ----

func TestScanClusterToolMetadata(t *testing.T) {
	tool := scanClusterTool(&fakeScanner{})
	if err := tool.Metadata.Validate(); err != nil {
		t.Fatalf("元数据五字段校验失败：%v", err)
	}
	if tool.Metadata.RiskHint != mcpserver.RiskR0 {
		t.Errorf("应为 R0 只读，got %q", tool.Metadata.RiskHint)
	}
	if !tool.Metadata.Idempotent {
		t.Error("只读巡检应幂等")
	}
	if tool.Metadata.TimeoutMs <= 0 {
		t.Error("应有超时上界")
	}
	// InputSchema 契约：object + 三个属性 + format enum。
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("InputSchema 缺 properties：%v", tool.InputSchema)
	}
	for _, k := range []string{"namespace", "format", "since"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema 缺属性 %q", k)
		}
	}
	// 注册进框架成功（元数据强校验通过）。
	reg := mcpserver.NewRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if reg.Len() != 1 {
		t.Errorf("注册表应 1 个工具，got %d", reg.Len())
	}
}
