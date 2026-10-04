// Package main 是 aegis L2 安全闸门主服务（gatekeeper）的装配入口。
//
// Phase 0 占位骨架：仅落地统一 main 骨架的启动期要素（flag 解析 → 配置合成 → slog 初始化，
// 退出码语义见下）。装配对象（MCP 代理、risk/policy/approval 等上下文接线）随 M2 起填充。
//
// 设计出处：cmd/README.md §4（统一 main 骨架九要素）、cmd/gatekeeper/README.md。
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

const processName = "gatekeeper"

// 退出码约定（cmd/README.md §5）：0 干净退出 / 1 运行期错误 / 2 启动期错误。
func main() {
	os.Exit(run())
}

func run() int {
	fs := pflag.NewFlagSet(processName, pflag.ContinueOnError)
	logLevel := fs.String("log-level", "", "slog level: debug|info|warn|error（env AEGIS_LOG_LEVEL > 默认 info）")
	listenAddr := fs.String("listen-addr", "", "probe listen 地址 host:port（env AEGIS_LISTEN_ADDR > 默认 :8080）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		// ① flag 解析失败：退出码 2，用法已由 pflag 输出
		return 2
	}

	// ② 配置合成：flag > env > 默认（cmd/README.md §4 第 2 步）
	level := firstNonEmpty(*logLevel, os.Getenv("AEGIS_LOG_LEVEL"), "info")
	addr := firstNonEmpty(*listenAddr, os.Getenv("AEGIS_LISTEN_ADDR"), ":8080")

	// ③ slog 初始化：JSON handler；配置非法一律退出码 2（fail-fast）
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
		fmt.Fprintf(os.Stderr, "invalid --listen-addr %q: %v\n", addr, err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	// ④–⑨ OTel 初始化 / 探针 / 依赖装配 / 就绪自检 / 信号监听：M1–M2 按里程碑填充。
	logger.Info("bootstrap complete (phase0 skeleton)",
		"process", processName,
		"config_source", "flag>env>default",
		"listen_addr", addr,
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
