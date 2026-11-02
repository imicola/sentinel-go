package main

// evictor_test.go：调用面测试——apiEvictor 发出的请求形状（UID 前置、子资源），
// 以及 fakeEvictor 的计数/回放语义。fake 只证明客户端行为，不模拟服务端准入。

import (
	"context"
	"errors"
	"testing"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// TestEvictor_APIRequestCarriesUIDPrecondition：真实 Evict() 代码路径经 fake clientset
// 发出的请求，必须是 pods/{name}/eviction 子资源、带 UID 前置。
func TestEvictor_APIRequestCarriesUIDPrecondition(t *testing.T) {
	cs := kubefake.NewSimpleClientset()
	var got *policyv1.Eviction
	var subresource, verb string
	cs.PrependReactor("create", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		verb = a.GetVerb()
		subresource = a.GetSubresource()
		got = a.(clienttesting.CreateAction).GetObject().(*policyv1.Eviction)
		return true, nil, nil
	})

	ev := NewAPIEvictor(cs)
	if err := ev.EvictPod(context.Background(), "ns-1", "pod-x", "uid-9"); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("没有发出 eviction 子资源请求")
	}
	if verb != "create" || subresource != "eviction" {
		t.Fatalf("请求形状 = %s pods/%s，期望 create pods/eviction", verb, subresource)
	}
	if got.Name != "pod-x" || got.Namespace != "ns-1" {
		t.Fatalf("目标 = %s/%s，期望 ns-1/pod-x", got.Namespace, got.Name)
	}
	if got.DeleteOptions == nil || got.DeleteOptions.Preconditions == nil ||
		got.DeleteOptions.Preconditions.UID == nil || *got.DeleteOptions.Preconditions.UID != types.UID("uid-9") {
		t.Fatalf("UID 前置缺失或不对: %+v", got.DeleteOptions)
	}
}

// TestEvictor_FakeCountsAndReplays：替身按序回放注入错误，之后返回 nil，并记下全部调用。
func TestEvictor_FakeCountsAndReplays(t *testing.T) {
	f := NewFakeEvictor(
		apierrors.NewTooManyRequestsError("budget"),
		apierrors.NewForbidden(schemaGroupResource("pods"), "pod-x", errors.New("no")),
	)
	if err := f.EvictPod(context.Background(), "ns-1", "pod-a", "uid-a"); !apierrors.IsTooManyRequests(err) {
		t.Fatalf("第 1 次应回放 429，got %v", err)
	}
	if err := f.EvictPod(context.Background(), "ns-1", "pod-a", "uid-a"); !apierrors.IsForbidden(err) {
		t.Fatalf("第 2 次应回放 403，got %v", err)
	}
	if err := f.EvictPod(context.Background(), "ns-1", "pod-b", "uid-b"); err != nil {
		t.Fatalf("错误耗尽后应返回 nil，got %v", err)
	}
	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("调用计数 = %d，期望 3", len(calls))
	}
	if calls[2].Name != "pod-b" || calls[2].UID != "uid-b" {
		t.Fatalf("第 3 次调用 = %+v，期望 pod-b/uid-b", calls[2])
	}
}

func schemaGroupResource(resource string) schema.GroupResource {
	return schema.GroupResource{Group: "", Resource: resource}
}
