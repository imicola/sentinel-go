package main

// evictor.go：驱逐调用面——窄接口 + 真实实现 + 测试替身。
// 装配关系：main 在同一 rest.Config 上创建 typed clientset 并注入 apiEvictor；
// Reconciler 只借用 PodEvictor 接口，不持有具体客户端。
// controller-runtime Client 继续负责持久对象读写；这次对 Pod 子资源的 POST
// 走 typed clientset 的 Evict()（POST /api/v1/namespaces/<ns>/pods/<name>/eviction）。

import (
	"context"
	"strings"
	"sync"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// PodEvictor：协调器需要的最小驱逐调用面。
// 返回 nil 只代表这次请求被受理，不代表终止已完成，更不代表替代已就绪。
type PodEvictor interface {
	// EvictPod：对 Pod 子资源发一次携带 UID 前置的 Eviction 请求。
	// 身份/归属变化时调用方不得调用（先重读核对）。
	EvictPod(ctx context.Context, namespace, name, uid string) error
}

// apiEvictor：真实实现——借 typed clientset。
type apiEvictor struct {
	cs kubernetes.Interface
}

// NewAPIEvictor：主入口装配时使用。
func NewAPIEvictor(cs kubernetes.Interface) PodEvictor {
	return &apiEvictor{cs: cs}
}

func (e *apiEvictor) EvictPod(ctx context.Context, namespace, name, uid string) error {
	u := types.UID(uid)
	return e.cs.PolicyV1().Evictions(namespace).Evict(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		DeleteOptions: &metav1.DeleteOptions{
			// 名字定位对象，UID 前置锁定身份：同名新对象不命中这次请求。
			Preconditions: &metav1.Preconditions{UID: &u},
		},
	})
}

// EvictCall：替身记下的一次调用。
type EvictCall struct {
	Namespace string
	Name      string
	UID       string
}

// isPdbRefusal：识别锁定版本 v1.36.5 驱逐实现的官方 PDB 拒绝消息
// （"…violate the pod's disruption budget…"——预算不足与 PDB 尚未观察到新代次
// 两个分支共用这条消息文本）。它是原因分类的依据；
// Retry-After 提示不是——PDB 观察落后的 429 同样带 10 秒提示（R6）。
func isPdbRefusal(err error) bool {
	return apierrors.IsTooManyRequests(err) &&
		strings.Contains(err.Error(), "violate the pod's disruption budget")
}

// isKnownEvictionMisconfiguration：识别已知的重叠 PDB 配置错误（500 结构化消息，
// 同样来自锁定版本源码）。已知配置拒绝与"网络结果未知"分开处理：
// 前者自动重试没有意义，交人工；不能把所有 5xx 武断判永久失败，
// 也不靠一律 err 重试冒充停止——只认这一条已知消息（R6）。
func isKnownEvictionMisconfiguration(err error) bool {
	return apierrors.IsInternalError(err) &&
		strings.Contains(err.Error(), "more than one PodDisruptionBudget")
}

// fakeEvictor：测试替身——只计数与回放注入的错误。
// 它不做真实 PDB 准入，也不模拟服务端；通过它只能断言客户端行为。
type fakeEvictor struct {
	mu     sync.Mutex
	calls  []EvictCall
	errors []error // 依次弹出；耗尽后返回 nil
}

// NewFakeEvictor：errors 依次作为每次调用的返回值。
func NewFakeEvictor(errors ...error) *fakeEvictor {
	return &fakeEvictor{errors: errors}
}

func (f *fakeEvictor) EvictPod(_ context.Context, namespace, name, uid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, EvictCall{Namespace: namespace, Name: name, UID: uid})
	if len(f.errors) == 0 {
		return nil
	}
	err := f.errors[0]
	f.errors = f.errors[1:]
	return err
}

// Calls：已发生调用的快照。
func (f *fakeEvictor) Calls() []EvictCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EvictCall(nil), f.calls...)
}
