package main

import (
	"context"
	"testing"
	"time"
)

type inspectingEvictor func(
	ctx context.Context, namespace, name, uid string,
) error

func (f inspectingEvictor) EvictPod(
	ctx context.Context, namespace, name, uid string,
) error {
	return f(ctx, namespace, name, uid)
}

func TestCooldown_EarlyReconcileDoesNotEvictAgain(t *testing.T) {
	ctx := context.Background()

	// 现场：疏散未完成，目标Pod仍在，身份和归属均有效。
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	pod := ownedPod(
		rs, "sentinel-aaa", "uid-pod-a",
		"worker-a", true, false,
	)
	c := newTestClient(
		plan, cordonedNode("uid-plan-1"), dep, rs, pod,
	)

	// 第一次驱逐返回429，并提示等待120秒。
	ev := NewFakeEvictor(pdbRefusalError(120))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev, Now: clock}

	// 第一轮必须真的走到驱逐，否则后面的零新增调用没有意义。
	if _, err := r.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}
	if got := len(ev.Calls()); got != 1 {
		t.Fatalf("第一轮驱逐调用数 = %d，期望1", got)
	}

	// 不等待、不修改对象，直接模拟一次提前唤醒。
	if _, err := r.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}

	// 协调发生了，但不应该产生第二次驱逐。
	if got := len(ev.Calls()); got != 1 {
		t.Fatalf("冷却期间累计驱逐调用数 = %d，期望仍为1", got)
	}
}

func TestCooldown_NewReconcilerRespectsSavedWait(t *testing.T) {
	ctx := context.Background()

	// 现场：疏散未完成，目标Pod仍在，身份和归属均有效。
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	pod := ownedPod(
		rs, "sentinel-aaa", "uid-pod-a",
		"worker-a", true, false,
	)
	c := newTestClient(
		plan, cordonedNode("uid-plan-1"), dep, rs, pod,
	)

	ev := NewFakeEvictor(pdbRefusalError(120))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev, Now: clock}
	// 第一轮必须真的走到驱逐，否则后面的零新增调用没有意义。
	if _, err := r.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}
	if got := len(ev.Calls()); got != 1 {
		t.Fatalf("第一轮驱逐调用数 = %d，期望1", got)
	}

	// 新协调器没有上一轮的任何内存字段。
	// 但仍连接同一份存储，能够读到第一轮保存的状态。
	ev2 := NewFakeEvictor()
	r2 := &MaintenancePlanReconciler{
		Client:  c,
		Evictor: ev2,
		Now:     clock,
	}

	if _, err := r2.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}

	// 新替身从零开始计数，本轮不应该调用它。
	if got := len(ev2.Calls()); got != 0 {
		t.Fatalf("重建协调器后仍在冷却期，驱逐调用数 = %d，期望0", got)
	}

}

func TestCooldown_ExpiryReenablesUntilDeadline(t *testing.T) {
	ctx := context.Background()
	// 现场：疏散未完成，目标Pod仍在，身份和归属均有效。
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	pod := ownedPod(
		rs, "sentinel-aaa", "uid-pod-a",
		"worker-a", true, false,
	)
	base := time.Now().UTC().Truncate(time.Second)
	nowV := base

	c := newTestClient(
		plan, cordonedNode("uid-plan-1"), dep, rs, pod,
	)
	ev := NewFakeEvictor(pdbRefusalError(120))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev, Now: func() time.Time { return nowV }}
	// t0：第一次驱逐被 429 拒，提示 120s；落库的 NextActionAt 应等于 base+120s
	//   （这一断言顺带钉住 writeDrainStatus 用提示延迟覆盖保底值）
	// nowV = base+121s：冷却已过 → 第二次驱逐放行，Calls 变 2
	// nowV = base+11m：超过 Deadline → 不再发新驱逐，Calls 仍 2，
	//   status = Blocked/drain-timeout-exceeded
	if _, err := r.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseDraining {
		t.Fatalf("expected Draining, got %v", got.Status.Phase)
	}
	if got.Status.Reason != "pdb-budget-exhausted" {
		t.Fatalf("expected pdb-budget-exhausted, got %v", got.Status.Reason)
	}
	// 第一次驱逐被 429 拒绝并提示等待 120s，落库的 NextActionAt 必须等于 base+120s。
	// *metav1.Time 内部是 *time.Time，先判空再取值比较，并用 Equal 避免单调时钟/位置差异。
	wantNext := base.Add(120 * time.Second)
	if got.Status.EvictionProgress.NextActionAt == nil {
		t.Fatalf("expected NextActionAt to be set, got nil")
	}
	if !got.Status.EvictionProgress.NextActionAt.Time.Equal(wantNext) {
		t.Fatalf("expected NextActionAt = %v, got %v", wantNext, got.Status.EvictionProgress.NextActionAt.Time)
	}

	if len(ev.Calls()) != 1 {
		t.Fatalf("expected 1 call, got %v", len(ev.Calls()))
	}

	nowV = base.Add(121 * time.Second)
	if _, err := r.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}
	got = getPlan(t, c)
	if got.Status.Phase != PhaseDraining {
		t.Fatalf("expected Draining, got %v", got.Status.Phase)
	}
	if got.Status.Reason != "eviction-request-accepted" {
		t.Fatalf("expected eviction-request-accepted, got %v", got.Status.Reason)
	}
	if len(ev.Calls()) != 2 {
		t.Fatalf("expected 2 calls, got %v", len(ev.Calls()))
	}

	nowV = base.Add(11 * time.Minute)
	if _, err := r.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}
	got = getPlan(t, c)
	if got.Status.Phase != PhaseBlocked {
		t.Fatalf("expected Blocked, got %v", got.Status.Phase)
	}
	if got.Status.Reason != "drain-timeout-exceeded" {
		t.Fatalf("expected drain-timeout-exceeded, got %v", got.Status.Reason)
	}
	if len(ev.Calls()) != 2 {
		t.Fatalf("expected 2 calls, got %v", len(ev.Calls()))
	}

}

// 调用前约束已保存

func TestCooldown_EvictionProgress(t *testing.T) {
	ctx := context.Background()
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	pod := ownedPod(
		rs, "sentinel-aaa", "uid-pod-a",
		"worker-a", true, false,
	)
	c := newTestClient(
		plan, cordonedNode("uid-plan-1"), dep, rs, pod,
	)
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	called := false

	ev := inspectingEvictor(func(
		ctx context.Context, namespace, name, uid string,
	) error {
		called = true

		// 正在执行驱逐调用，此时协调器还没有处理返回结果。
		// 从client读，才能证明状态已经保存，而非只改了内存。
		saved := getPlan(t, c)
		progress := saved.Status.EvictionProgress
		if progress == nil || progress.NextActionAt == nil {
			t.Fatal("调用驱逐前，冷却约束尚未保存")
		}

		want := now.Add(requeueAfter)
		if !progress.NextActionAt.Time.Equal(want) {
			t.Fatalf("调用前NextActionAt = %v，期望 %v",
				progress.NextActionAt.Time, want)
		}

		return nil
	})

	r := &MaintenancePlanReconciler{
		Client:  c,
		Evictor: ev,
		Now:     clock,
	}

	if _, err := r.Reconcile(ctx, key()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("没有进入驱逐调用，未执行顺序检查")
	}
}
