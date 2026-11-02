package main

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPdbRetry_Unknown429Hint(t *testing.T) {
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	dep := ownedDeployment(1, 1)
	pod := ownedPod(
		rsFor(dep),
		"sentinel-aaa", "uid-pod-a", "worker-a",
		true, false,
	)
	c := newTestClient(plan, pod)

	// 替身只返回这个错误，不模拟真实PDB准入。
	ev := NewFakeEvictor(retryHintError(3))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}

	// 从客户端取出申请，使用真实的持久状态版本。
	plan = getPlan(t, c)
	d := DrainDecision{
		Evict: &ActivePodRef{
			Namespace: "maintenance-lab",
			Name:      "sentinel-aaa",
			UID:       "uid-pod-a",
		},
	}

	res, err := r.evictOne(
		context.Background(), logr.Discard(), plan, d,
	)
	if err != nil {
		t.Fatal(err)
	}

	got := getPlan(t, c)
	if got.Status.Phase != PhaseDraining ||
		got.Status.Reason != "eviction-throttled" {
		t.Fatalf("持久报告 = %s/%s",
			got.Status.Phase, got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatal("明确拒绝后不应残留在途身份")
	}
	if res.RequeueAfter != 3*time.Second {
		t.Fatalf("重排队间隔 = %v，期望3秒", res.RequeueAfter)
	}
	if len(ev.Calls()) != 1 {
		t.Fatalf("本轮调用数 = %d，期望1", len(ev.Calls()))
	}
}

func TestPdbRetry_Explicit429Hint(t *testing.T) {
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	dep := ownedDeployment(1, 1)
	pod := ownedPod(
		rsFor(dep),
		"sentinel-aaa", "uid-pod-a", "worker-a",
		true, false,
	)
	c := newTestClient(plan, pod)

	ev := NewFakeEvictor(pdbRefusalError(0))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}

	plan = getPlan(t, c)
	d := DrainDecision{
		Evict: &ActivePodRef{
			Namespace: "maintenance-lab",
			Name:      "sentinel-aaa",
			UID:       "uid-pod-a",
		},
	}

	res, err := r.evictOne(
		context.Background(),
		logr.Discard(),
		plan,
		d,
	)
	if err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseDraining ||
		got.Status.Reason != "pdb-budget-exhausted" {
		t.Fatalf("持久报告 = %s/%s",
			got.Status.Phase, got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatal("明确拒绝后不应残留在途身份")
	}

	if res.RequeueAfter != requeueAfter {
		t.Fatalf("重排队间隔 = %v，期望30秒", res.RequeueAfter)
	}
	if len(ev.Calls()) != 1 {
		t.Fatalf("本轮调用数 = %d，期望1", len(ev.Calls()))
	}

}

func TestPdbRetry_DoNotByPassPdb(t *testing.T) {
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	dep := ownedDeployment(1, 1)
	pod := ownedPod(
		rsFor(dep),
		"sentinel-aaa", "uid-pod-a", "worker-a",
		true, false,
	)
	c := newTestClient(plan, pod)

	ev := NewFakeEvictor(pdbRefusalError(0))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}

	plan = getPlan(t, c)
	d := DrainDecision{
		Evict: &ActivePodRef{
			Namespace: "maintenance-lab",
			Name:      "sentinel-aaa",
			UID:       "uid-pod-a",
		},
	}
	res, err := r.evictOne(
		context.Background(),
		logr.Discard(),
		plan,
		d,
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != requeueAfter {
		t.Fatalf("重排队间隔 = %v，期望30秒", res.RequeueAfter)
	}
	if len(ev.Calls()) != 1 {
		t.Fatalf("本轮调用数 = %d，期望1", len(ev.Calls()))
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseDraining ||
		got.Status.Reason != "pdb-budget-exhausted" {
		t.Fatalf("持久报告 = %s/%s",
			got.Status.Phase, got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatal("明确拒绝后不应残留在途身份")
	}
	after := &corev1.Pod{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), after); err != nil {
		t.Fatal(err)
	}
	if after.UID != pod.UID {
		t.Fatal("拒绝后Pod身份不应改变")
	}
	if after.DeletionTimestamp != nil {
		t.Fatal("拒绝后不应由协调器将Pod标记删除")
	}

}

func TestPdbRetry_RefuseToAccept(t *testing.T) {
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	dep := ownedDeployment(1, 1)
	pod := ownedPod(
		rsFor(dep),
		"sentinel-aaa", "uid-pod-a", "worker-a",
		true, false,
	)
	c := newTestClient(plan, pod)

	ev := NewFakeEvictor(pdbRefusalError(0), nil)
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}

	plan = getPlan(t, c)
	d := DrainDecision{
		Evict: &ActivePodRef{
			Namespace: "maintenance-lab",
			Name:      "sentinel-aaa",
			UID:       "uid-pod-a",
		},
	}
	res, err := r.evictOne(
		context.Background(),
		logr.Discard(),
		plan,
		d,
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != requeueAfter {
		t.Fatalf("重排队间隔 = %v，期望30秒", res.RequeueAfter)
	}
	if len(ev.Calls()) != 1 {
		t.Fatalf("本轮调用数 = %d，期望1", len(ev.Calls()))
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseDraining ||
		got.Status.Reason != "pdb-budget-exhausted" {
		t.Fatalf("持久报告 = %s/%s",
			got.Status.Phase, got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatal("明确拒绝后不应残留在途身份")
	}

	// 第二轮允许
	plan = getPlan(t, c)
	r = &MaintenancePlanReconciler{Client: c, Evictor: ev}
	res, err = r.evictOne(
		context.Background(),
		logr.Discard(),
		plan,
		d,
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != requeueAfter {
		t.Fatalf("重排队间隔 = %v，期望30秒", res.RequeueAfter)
	}
	if len(ev.Calls()) != 2 {
		t.Fatalf("本轮调用数 = %d，期望2", len(ev.Calls()))
	}
	got = getPlan(t, c)
	if got.Status.Phase != PhaseDraining ||
		got.Status.Reason != "eviction-request-accepted" {
		t.Fatalf("持久报告 = %s/%s",
			got.Status.Phase, got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod == nil {
		t.Fatal("明确接受后应残留在途身份")
	}
	if got.Status.EvictionProgress.ActivePod.UID != "uid-pod-a" {
		t.Fatalf("在途身份 = %s，期望uid-pod-a",
			got.Status.EvictionProgress.ActivePod.UID)
	}
	for i, call := range ev.Calls() {
		if call.Namespace != d.Evict.Namespace ||
			call.Name != d.Evict.Name ||
			call.UID != d.Evict.UID {
			t.Fatalf("第%d次请求身份不符: %+v", i+1, call)
		}
	}
}
