// Package main 是 aegis 用户交互 CLI 的装配入口。
//
// 默认（无子命令）进入交互式会话：本地 Tier-0 scan 与 kagent A2A 只读对话
// 在同一个 prompt 内；aegis-cli 仅为视图/提交器，写操作仍经 gatekeeper。
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "aegis-cli",
		Short: "aegis 用户交互 CLI（默认进入交互式只读会话）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return newSession(cmd).run()
		},
	}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "打印版本信息",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "aegis-cli v0.0.0-phase0")
			return err
		},
	})
	root.AddCommand(newChatCmd())         // 兼容显式 chat；默认入口见 newSession
	root.AddCommand(newScanCmd())         // Tier-0 确定性巡检（离线零 LLM）
	root.AddCommand(newReplayCmd())       // 审计回放
	root.AddCommand(newApproveCmd(true))  // 审批通过
	root.AddCommand(newApproveCmd(false)) // 审批拒绝
	return root
}
