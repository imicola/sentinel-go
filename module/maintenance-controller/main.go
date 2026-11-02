package main

// main.go：装配层——W43 形状 + typed clientset 与驱逐调用面。
// main 创建并持有 Manager 与 clientset；Reconciler 注册进 Manager、被按键唤醒、
// 借用 Client（持久对象读写）与 PodEvictor（驱逐子资源 POST）。
//
// 学习时运行（制作期集群停止，未实跑）：
//
//	go run . --kubeconfig <路径> --context kind-work-maintenance
//
// 前提：MaintenancePlan CRD v3 已 apply（见 k8s/maintenanceplan-crd-v3.yaml）。
// Ctrl+C 退出——只是本次前台进程；归还流程见 lab README。

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	// 参数用独立 FlagSet 解析——不碰全局 flag.CommandLine（0020 的坑）。
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	var (
		kubeconfig  = fs.String("kubeconfig", "", "kubeconfig 文件路径（必填）")
		contextName = fs.String("context", "", "context 名（必填，不回退默认值）")
	)
	fs.Parse(os.Args[1:])
	if *kubeconfig == "" || *contextName == "" {
		fmt.Fprintln(os.Stderr, "--kubeconfig 与 --context 必填（显式连接，不回退默认值）")
		fs.Usage()
		os.Exit(1)
	}
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	restCfg, err := loadConfig(*kubeconfig, *contextName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "装配连接失败:", err)
		os.Exit(1)
	}
	scheme := mustScheme()
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建 Manager 失败:", err)
		os.Exit(1)
	}
	// Pod 落点索引：ReadFacts 用 field selector spec.nodeName 只读目标节点上的 Pod。
	// 观察跨 namespace（节点上有什么与它在哪个 ns 无关——R1 修订），与 Manager
	// 默认的集群级缓存（List/Watch 不限 ns）一致；只读权限清单见 k8s/rbac-eviction.yaml。
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName",
		func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.NodeName} }); err != nil {
		fmt.Fprintln(os.Stderr, "注册 Pod 落点索引失败:", err)
		os.Exit(1)
	}
	// typed clientset：驱逐子资源走它，与 controller-runtime Client 同一 rest.Config。
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建 clientset 失败:", err)
		os.Exit(1)
	}
	err = builder.ControllerManagedBy(mgr).
		For(&MaintenancePlan{}).
		Complete(&MaintenancePlanReconciler{Client: mgr.GetClient(), Evictor: NewAPIEvictor(clientset)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "注册控制器失败:", err)
		os.Exit(1)
	}
	fmt.Println("MaintenancePlan 协调器已启动（Ctrl+C 退出；受控 cordon + 显式许可的疏散）")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintln(os.Stderr, "运行失败:", err)
		os.Exit(1)
	}
}

// loadConfig：与 0016/0021 同一纪律——显式 kubeconfig + 显式 context，拼错报可用名字。
func loadConfig(kubeconfigPath, contextName string) (*rest.Config, error) {
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	raw, err := loadingRules.Load()
	if err != nil {
		return nil, fmt.Errorf("读取 kubeconfig: %w", err)
	}
	if _, ok := raw.Contexts[contextName]; !ok {
		names := make([]string, 0, len(raw.Contexts))
		for n := range raw.Contexts {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("context %q 不存在；可用: %v", contextName, names)
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: contextName}).ClientConfig()
}

// mustScheme：内置类型 + MaintenancePlan。
func mustScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := AddToScheme(scheme); err != nil {
		panic(err)
	}
	return scheme
}
