// Package main 是 aegis CRD controller 的装配入口。
//
// Phase 0 占位骨架：启动期要素（flag → 配置 → slog）与 gatekeeper 同构，
// 差异仅在第 ⑦ 步装配对象（reconcile manager + leader election，M2 T2.1 接入）。
//
// 设计出处：cmd/README.md §4；cmd/controller/README.md。
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

const processName = "controller"

func main() {
	os.Exit(run())
}

func run() int {
	fs := pflag.NewFlagSet(processName, pflag.ContinueOnError)
	logLevel := fs.String("log-level", "", "slog level: debug|info|warn|error（env AEGIS_LOG_LEVEL > 默认 info）")
	metricsAddr := fs.String("metrics-addr", "", "metrics listen 地址 host:port（env AEGIS_METRICS_ADDR > 默认 :8081）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	level := firstNonEmpty(*logLevel, os.Getenv("AEGIS_LOG_LEVEL"), "info")
	addr := firstNonEmpty(*metricsAddr, os.Getenv("AEGIS_METRICS_ADDR"), ":8081")

	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "invalid --log-level %q: want debug|info|warn|error\n", level)
		return 2
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --metrics-addr %q: %v\n", addr, err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	// 装配对象（manager、cache、reconcile 接线）随 M2（T2.1）填充；
	// controller-runtime 的 healthz/readyz 挂载与 leader election 同任务接入。
	logger.Info("bootstrap complete (phase0 skeleton)",
		"process", processName,
		"config_source", "flag>env>default",
		"metrics_addr", addr,
		"exit_code", 0,
	)
	return 0
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
