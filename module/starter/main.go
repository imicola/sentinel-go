package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// main 只做三件事：装配连接、让 Reader 取事实、把事实交给输出层。
// 判断不在这里——0017 会加 analyze.go 并把输出段换成 Report。
func main() {
	var (
		kubeconfig  = flag.String("kubeconfig", "", "kubeconfig 文件路径（必填）")
		contextName = flag.String("context", "", "context 名（必填，不回退默认值）")
		namespace   = flag.String("namespace", "", "目标 Deployment 所在命名空间（必填）")
		node        = flag.String("node", "", "目标节点名（必填）")
		deployment  = flag.String("deployment", "", "目标 Deployment 名（必填）")
	)
	flag.Parse()

	// 参数校验：少一个就直接退出，不猜、不回退。
	missing := make([]string, 0, 5)
	for name, v := range map[string]string{
		"--kubeconfig": *kubeconfig, "--context": *contextName, "--namespace": *namespace,
		"--node": *node, "--deployment": *deployment,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintln(os.Stderr, "缺少必填参数:", strings.Join(missing, ", "))
		os.Exit(2)
	}
	if _, err := os.Stat(*kubeconfig); err != nil {
		fmt.Fprintln(os.Stderr, "kubeconfig 文件不可访问:", err)
		os.Exit(2)
	}

	// 连接装配：显式 kubeconfig + 显式 context。context 名先核对再建连接，拼错直接报错。
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig}
	raw, err := loadingRules.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 kubeconfig 失败:", err)
		os.Exit(1)
	}
	if _, ok := raw.Contexts[*contextName]; !ok {
		names := make([]string, 0, len(raw.Contexts))
		for n := range raw.Contexts {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(os.Stderr, "context %q 不存在；可用: %v\n", *contextName, names)
		os.Exit(1)
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: *contextName}).ClientConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "构造 rest.Config 失败:", err)
		os.Exit(1)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "构造 clientset 失败:", err)
		os.Exit(1)
	}

	// 有界的 ctx：API 卡住时这里先到期，错误沿调用链返回；凭据一个字节都不打印。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reader := NewKubeReader(cs, ReaderParams{Namespace: *namespace, Node: *node, Deployment: *deployment})

	// ---- 输出层：这一段负责错误与呈现，不负责判断 ----
	// 0017 阶段：在这里加 report := Analyze(snap)，把输出换成 Report（Verdict + Reasons + 时间）。
	code, err := RunPreflight(ctx, reader, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "输出 JSON 失败:", err)
		os.Exit(1)
	}
	os.Exit(code)

}
