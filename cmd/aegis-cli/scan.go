package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	k8sadapter "github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/scan"
)

// scan 子命令：Tier-0 确定性规则巡检（离线、零 LLM、零 token，I11）。
// 仅装配（cmd 纪律）：取数经 internal/adapters/k8s 端口，判定在 internal/scan 引擎，
// 本文件只做依赖注入与输出渲染，不含业务逻辑。

func newScanCmd() *cobra.Command {
	var (
		kubeconfig  string
		namespace   string
		policiesDir string
		format      string
		output      string
		sinceSec    int64
	)
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "确定性规则巡检（Tier-0）：高频故障根因 + 结构缺陷 lint，离线零 LLM",
		Long: "对集群做确定性规则巡检：A 层运行时故障根因（OOM/CrashLoop/Pending/驱逐等）" +
			" + B 层 YAML 结构 lint。全程离线、零 LLM 调用，断网/气隙环境照常工作（I11）。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScan(cmd, kubeconfig, namespace, policiesDir, format, output, sinceSec)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", defaultKubeconfig(), "kubeconfig 路径")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "限定命名空间（空=全集群，仍受上限约束）")
	cmd.Flags().StringVar(&policiesDir, "policies", "", "B 层规则目录（热修改；空=用嵌入默认规则）")
	cmd.Flags().StringVarP(&format, "format", "f", "text", "输出格式：text|json|sarif")
	cmd.Flags().StringVarP(&output, "output", "o", "", "输出文件（空=stdout）")
	cmd.Flags().Int64Var(&sinceSec, "since", 86400, "事件时间窗（秒，仅取近期事件作佐证；0=不限）")
	return cmd
}

func runScan(cmd *cobra.Command, kubeconfig, namespace, policiesDir, format, output string, sinceSec int64) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("加载 kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("构造 clientset: %w", err)
	}
	// 装配：适配层取数端口 + B 层规则来源 + 引擎（A 层默认注册表）。
	provider := k8sadapter.NewScanSnapshotProvider(cs)
	svc := scan.NewService(provider, FilePolicySource{Dir: policiesDir}, nil, 0, nil)

	res, err := svc.Scan(cmd.Context(), scan.CollectOptions{Namespace: namespace, SinceSeconds: sinceSec})
	if err != nil {
		return err
	}

	w, closeFn, err := scanOutput(cmd, output)
	if err != nil {
		return err
	}
	defer closeFn()

	switch format {
	case "json":
		return scan.WriteJSON(w, res)
	case "sarif":
		return scan.WriteSARIF(w, res)
	case "text", "":
		return scan.WriteText(w, res)
	default:
		return fmt.Errorf("未知输出格式 %q（text|json|sarif）", format)
	}
}

// scanOutput 打开输出目标（文件或 stdout）。
func scanOutput(cmd *cobra.Command, output string) (io.Writer, func(), error) {
	if output == "" {
		return cmd.OutOrStdout(), func() {}, nil
	}
	f, err := os.Create(output)
	if err != nil {
		return nil, func() {}, fmt.Errorf("创建输出文件: %w", err)
	}
	return f, func() { f.Close() }, nil
}

// defaultKubeconfig 默认 ~/.kube/config。
func defaultKubeconfig() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}
