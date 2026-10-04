// Package main 是 aegis 用户交互 CLI 的装配入口。
//
// Phase 0 占位骨架：空 root command + version 子命令。
// replay（M1 T1.8）、approve/deny（M2 T2.4）等子命令后续挂载；CLI 仅为视图，无服务端逻辑。
//
// 设计出处：cmd/README.md §4；cmd/aegis-cli/README.md。
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "aegis-cli",
		Short: "aegis 用户交互 CLI（视图与提交，无服务端逻辑）",
		// 无 Run：执行时打印用法（cobra 默认）
	}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "打印版本信息",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "aegis-cli v0.0.0-phase0")
			return err
		},
	})
	if err := root.Execute(); err != nil {
		// cobra 已打印错误与用法；业务退出码扩展见 cmd/aegis-cli/README.md
		os.Exit(1)
	}
}
