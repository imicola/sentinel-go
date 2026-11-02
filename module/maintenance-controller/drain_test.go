package main

// drain_test.go：W44 疏散阶段替身测试——进入点、一次 UID 绑定调用、返回分类、
// 完成判据与恢复行为。替身只证明对话方式与判断逻辑；真实准入与调度未在替身层模拟。

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ---- 进入点（装配给定部分的行为）----

// TestDrain_NotAllowedStaysCordoned：不带 allowEviction 的旧申请止于 Cordoned——
// 升级程序不能让历史 cordon 申请自动开始驱逐。
func TestDrain_NotAllowedStaysCordoned(t *testing.T) {
	plan := testPlan("uid-plan-1")
	plan.Status.Phase = PhaseCordoned
	plan.Status.Reason = "cordon-already-applied"
	plan.Status.CordonIntent = &CordonIntent{NodeUID: nodeUID}
	plan.Status.ObservedGeneration = 1
	c := newTestClient(plan, cordonedNode("uid-plan-1"), ownedDeployment(1, 1))

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseCordoned || got.Status.EvictionProgress != nil {
		t.Fatalf("status = %s/progress=%v，期望停在 Cordoned 且无疏散进度", got.Status.Phase, got.Status.EvictionProgress)
	}
	if len(ev.Calls()) != 0 {
		t.Fatal("无许可时不得发出任何驱逐请求")
	}
}

// TestDrain_StartSavesProgressBeforeAnyCall：进入疏散先保存绑定身份与固定截止，
// 本轮不发起调用——先存身份，再动作。
func TestDrain_StartSavesProgressBeforeAnyCall(t *testing.T) {
	plan := testPlan("uid-plan-1")
	plan.Spec.AllowEviction = true
	plan.Status.Phase = PhaseCordoned
	plan.Status.Reason = "cordon-already-applied"
	plan.Status.CordonIntent = &CordonIntent{NodeUID: nodeUID}
	plan.Status.ObservedGeneration = 1
	c := newTestClient(plan, cordonedNode("uid-plan-1"), ownedDeployment(1, 1))

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	prog := got.Status.EvictionProgress
	if prog == nil {
		t.Fatal("进入疏散应先保存进度")
	}
	if prog.PlanGeneration != 1 || prog.DeploymentUID != depUID {
		t.Fatalf("进度绑定 = gen %d/dep %s，期望 1/%s", prog.PlanGeneration, prog.DeploymentUID, depUID)
	}
	window := time.Duration(DefaultDrainTimeoutSeconds)*time.Second - 30*time.Second
	if d := time.Until(prog.Deadline.Time); d < window || d > time.Duration(DefaultDrainTimeoutSeconds)*time.Second+30*time.Second {
		t.Fatalf("默认截止应在 %s 附近，got %v", time.Duration(DefaultDrainTimeoutSeconds)*time.Second, d)
	}
	if got.Status.Phase != PhaseDraining || got.Status.Reason != "drain-started" {
		t.Fatalf("status = %s/%s，期望 Draining/drain-started", got.Status.Phase, got.Status.Reason)
	}
	if len(ev.Calls()) != 0 {
		t.Fatal("保存进度的一轮不应发起驱逐调用")
	}
}

// ---- 一次调用与返回分类（本人接入的主体）----

// TestDrain_EvictsOnePodUIDBound：多个受支持 Pod 时一轮只驱逐一个（名字序），
// 请求携带该 Pod 的 UID；在途身份随之保存。
func TestDrain_EvictsOnePodUIDBound(t *testing.T) {
	dep := ownedDeployment(2, 2)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-a", "worker-a", true, false),
		ownedPod(rs, "sentinel-bbb", "uid-pod-b", "worker-a", true, false))

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	calls := ev.Calls()
	if len(calls) != 1 {
		t.Fatalf("一轮至多一次驱逐，got %d 次", len(calls))
	}
	if calls[0].Name != "sentinel-aaa" || calls[0].UID != "uid-pod-a" || calls[0].Namespace != "maintenance-lab" {
		t.Fatalf("调用 = %+v，期望 maintenance-lab/sentinel-aaa/uid-pod-a", calls[0])
	}
	got := getPlan(t, c)
	if got.Status.Reason != "eviction-request-accepted" {
		t.Fatalf("reason = %s，期望 eviction-request-accepted", got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod == nil || got.Status.EvictionProgress.ActivePod.UID != "uid-pod-a" {
		t.Fatalf("在途身份应随受理保存: %+v", got.Status.EvictionProgress.ActivePod)
	}
}

// TestDrain_RejectionDoesNotReportComplete（红队①：拒绝不报完成）——TODO(step-2)：
// 这组输入与断言由你来写，用来抓住下面这个坏实现：
//
//	坏实现A：把 429 当成"驱逐已发生"，status 直接写 WorkloadEvacuated。
//
// 可用的搭手：NewFakeEvictor(pdbRefusalError(0)) 注入官方消息的 PDB 拒绝；
// 断言至少覆盖：status 不进入 WorkloadEvacuated、ActivePod 不残留、同轮不连发。
// 先写输入再写断言；写完跑一条红队再写下一红队。
func TestDrain_RejectionDoesNotReportComplete(t *testing.T) {
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

	ev := NewFakeEvictor(pdbRefusalError(0))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}

	res, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if len(ev.Calls()) != 1 {
		t.Fatalf("应发出一次驱逐请求，got %d 次", len(ev.Calls()))
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("429后应延迟重新协调，got %+v", res)
	}

	if got.Status.Phase != PhaseDraining {
		t.Fatalf("status = %s，期望 PhaseDraining（拒绝不报完成）", got.Status.Phase)
	}
	if got.Status.Reason != "pdb-budget-exhausted" {
		t.Fatalf("reason = %s，期望 pdb-budget-exhausted）", got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatalf("在途身份应清空，got %+v", got.Status.EvictionProgress.ActivePod)
	}
	if got.Status.EvictionProgress.PlanGeneration != 1 || got.Status.EvictionProgress.DeploymentUID != depUID {
		t.Fatalf("进度绑定 = gen %d/dep %s，期望 1/%s", got.Status.EvictionProgress.PlanGeneration, got.Status.EvictionProgress.DeploymentUID, depUID)
	}
}

// TestDrain_RejectionWithHintUsesBoundedHint：429 带 Retry-After 提示——
// 提示只决定重排队间隔（有界）；原因分类不看提示：消息认不出官方 PDB 拒绝
// 文本时用中性 reason（R6：PDB 观察落后的 429 同样带提示，提示不是来源证明）。
func TestDrain_RejectionWithHintUsesBoundedHint(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-a", "worker-a", true, false))

	ev := NewFakeEvictor(retryHintError(3))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	res, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 3*time.Second {
		t.Fatalf("RequeueAfter = %v，期望按提示 3s", res.RequeueAfter)
	}
	got := getPlan(t, c)
	if got.Status.Reason != "eviction-throttled" {
		t.Fatalf("reason = %s，期望 eviction-throttled（来源不明，不按提示猜限流）", got.Status.Reason)
	}
}

// TestDrain_DoesNotBypassPdb（红队②：不绕过 PDB）——TODO(step-2)：
// 抓住坏实现B：收到 429 后不走 Eviction 而改走直接 Delete/Update（绕过预算），
// 或同轮连发多次请求硬闯。
// 提示：替身只记录 EvictPod 调用；对"改道"的断言要盯住 Pod 对象本身没被协调器动过
// （fake 世界里对象只有协调器会写），以及调用计数没有超过一次。
func TestDrain_DoesNotBypassPdb(t *testing.T) {
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

	ev := NewFakeEvictor(pdbRefusalError(0))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	var gotPod corev1.Pod
	if err := c.Get(context.Background(), clientKey("sentinel-aaa"), &gotPod); err != nil {
		t.Fatal(err)
	}
	res, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if len(ev.Calls()) != 1 {
		t.Fatalf("应发出一次驱逐请求，got %d 次", len(ev.Calls()))
	}

	if res.RequeueAfter <= 0 {
		t.Fatalf("429后应延迟重新协调，got %+v", res)
	}

	if got.Status.Phase != PhaseDraining {
		t.Fatalf("status = %s，期望 PhaseDraining（拒绝不报完成）", got.Status.Phase)
	}
	if got.Status.Reason != "pdb-budget-exhausted" {
		t.Fatalf("reason = %s，期望 pdb-budget-exhausted）", got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatalf("在途身份应清空，got %+v", got.Status.EvictionProgress.ActivePod)
	}

	// pod 依旧存在
	var afterPod corev1.Pod
	if err := c.Get(context.Background(), clientKey("sentinel-aaa"), &afterPod); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotPod, afterPod) {
		t.Fatalf("Pod 不应被协调器动过，got %+v，期望 %+v", afterPod, gotPod)
	}
}

// TestDrain_BudgetRecoversThenProceeds（红队③：恢复条件后再判断）——TODO(step-2)：
// 抓住坏实现C：一次 429 之后永久停在"预算阻塞"，条件恢复也不继续。
// 提示：NewFakeEvictor(err1) 的错误队列只回放一次——第二轮调用返回 nil，
// 正好构造"先拒后允"的条件变化；断言第二轮之后的 status 与调用计数。
func TestDrain_BudgetRecoversThenProceeds(t *testing.T) {
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

	ev := NewFakeEvictor(pdbRefusalError(0))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	res, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("429后应延迟重新协调，got %+v", res)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseDraining {
		t.Fatalf("status = %s，期望 PhaseDraining（拒绝不报完成）", got.Status.Phase)
	}
	if got.Status.Reason != "pdb-budget-exhausted" {
		t.Fatalf("reason = %s，期望 pdb-budget-exhausted）", got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatalf("在途身份应清空，got %+v", got.Status.EvictionProgress.ActivePod)
	}
	if len(ev.Calls()) != 1 {
		t.Fatalf("应发出一次驱逐请求，got %d 次", len(ev.Calls()))
	}
	// 第二轮模拟条件恢复：EvictPod 返回 nil，允许驱逐。
	var beforePod corev1.Pod
	if err := c.Get(context.Background(), clientKey("sentinel-aaa"), &beforePod); err != nil {
		t.Fatal(err)
	}

	res2, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	if res2.RequeueAfter <= 0 {
		t.Fatalf("429后应延迟重新协调，got %+v", res2)
	}
	got2 := getPlan(t, c)
	if got2.Status.Phase != PhaseDraining {
		t.Fatalf("第二轮允许驱逐后 status = %s，期望 Draining", got2.Status.Phase)
	}
	if got2.Status.Reason != "eviction-request-accepted" {
		t.Fatalf("第二轮允许驱逐后 reason = %s，期望 eviction-request-accepted", got2.Status.Reason)
	}
	if got2.Status.EvictionProgress.ActivePod == nil || got2.Status.EvictionProgress.ActivePod.UID != "uid-pod-a" {
		t.Fatalf("第二轮允许驱逐后在途身份应保存，got %+v", got2.Status.EvictionProgress.ActivePod)
	}

	afterPod := corev1.Pod{}
	if err := c.Get(context.Background(), clientKey("sentinel-aaa"), &afterPod); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforePod, afterPod) {
		t.Fatalf("第二轮允许驱逐后 Pod 不应被协调器动过，got %+v，期望 %+v", afterPod, beforePod)
	}
	calls := ev.Calls()
	if len(calls) != 2 {
		t.Fatalf("两轮应累计调用2次，got %d", len(calls))
	}
	if calls[1].Namespace != pod.Namespace ||
		calls[1].Name != pod.Name ||
		calls[1].UID != string(pod.UID) {
		t.Fatalf("第二轮目标不符：%+v", calls[1])
	}
}

// TestDrain_ForbiddenBlocksAndStaysBlocked：403 → Blocked 交人工；
// 之后即使替身放行，人工处理前也不再自动请求（盲重试禁止）。
func TestDrain_ForbiddenBlocksAndStaysBlocked(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-a", "worker-a", true, false))

	ev := NewFakeEvictor(apierrors.NewForbidden(
		schemaGroupResource("pods"), "sentinel-aaa", errString("forbidden")))
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	res, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatal("403 不安排自动重试")
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseBlocked || got.Status.Reason != "eviction-forbidden" {
		t.Fatalf("status = %s/%s，期望 Blocked/eviction-forbidden", got.Status.Phase, got.Status.Reason)
	}
	// 第二轮（替身已放行）：人工处理前保持 Blocked，不再调用。
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	got = getPlan(t, c)
	if got.Status.Reason != "eviction-forbidden" || len(ev.Calls()) != 1 {
		t.Fatalf("403 后不得盲重试：reason=%s calls=%d", got.Status.Reason, len(ev.Calls()))
	}
}

// TestDrain_TerminatingWaitsNoSecondEvict：在途旧 Pod 仍在且 Terminating——
// 等待，不重发、不选下一个。
func TestDrain_TerminatingWaitsNoSecondEvict(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	plan.Status.EvictionProgress.ActivePod = &ActivePodRef{
		Namespace: "maintenance-lab", Name: "sentinel-aaa", UID: "uid-pod-a"}
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-a", "worker-a", true, true)) // Terminating

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	if len(ev.Calls()) != 0 {
		t.Fatalf("Terminating 中不得重发/选下一个，got %d 次", len(ev.Calls()))
	}
	got := getPlan(t, c)
	if got.Status.Reason != "old-pod-terminating" {
		t.Fatalf("reason = %s，期望 old-pod-terminating", got.Status.Reason)
	}
}

// TestDrain_ActivePodUIDChangedNoReEvict：在途名字还在、UID 已换——
// 不请求旧目标、也不把这个同名新对象当旧目标驱逐；清在途，下一轮重新评估。
func TestDrain_ActivePodUIDChangedNoReEvict(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	plan.Status.EvictionProgress.ActivePod = &ActivePodRef{
		Namespace: "maintenance-lab", Name: "sentinel-aaa", UID: "uid-pod-OLD"}
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-NEW", "worker-a", true, false)) // 同名不同 UID

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	if len(ev.Calls()) != 0 {
		t.Fatalf("同名不同 UID 的过渡轮不得发出请求，got %d 次", len(ev.Calls()))
	}
	got := getPlan(t, c)
	if got.Status.Reason != "active-pod-uid-changed" {
		t.Fatalf("reason = %s，期望 active-pod-uid-changed", got.Status.Reason)
	}
	if got.Status.EvictionProgress.ActivePod != nil {
		t.Fatal("在途记录应清空")
	}
}

// TestDrain_TimeoutStopsNewEvictions：固定截止已过——停止新驱逐、Blocked 交人工；
// "重启"（再协调一轮）后仍保持停止，不自动 uncordon。
func TestDrain_TimeoutStopsNewEvictions(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", -time.Minute) // 截止已过
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-a", "worker-a", true, false))

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	res, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Calls()) != 0 {
		t.Fatalf("截止后不得再发驱逐，got %d 次", len(ev.Calls()))
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseBlocked || got.Status.Reason != "drain-timeout-exceeded" {
		t.Fatalf("status = %s/%s，期望 Blocked/drain-timeout-exceeded", got.Status.Phase, got.Status.Reason)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatal("截止后不自动重试")
	}
	// 第二轮模拟重启后：仍停止新动作，节点保持 cordon。
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	if len(ev.Calls()) != 0 {
		t.Fatal("重启后仍不得发新驱逐")
	}
	if n := getNode(t, c); !n.Spec.Unschedulable {
		t.Fatal("截止停机不自动 uncordon")
	}
}

// ---- 完成判据（本人实现的主体）----

// TestDrain_CompleteWhenOldGoneAndReplacementReady：旧身份消失、无受支持 Pod、
// 观察追上 spec、**实际替代 Pod**在非目标节点 Ready（R3：完成判据数真实 Pod，
// 不拿 Deployment 的 Ready 计数差冒充）——即使 PDB 预算已降为 0 也确认完成。
func TestDrain_CompleteWhenOldGoneAndReplacementReady(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	plan.Status.EvictionProgress.ActivePod = &ActivePodRef{
		Namespace: "maintenance-lab", Name: "sentinel-aaa", UID: "uid-pod-a"}
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs, pdbZero("maintenance-lab"), // 预算 0
		ownedPod(rs, "sentinel-bbb", "uid-pod-b", "worker-b", true, false)) // 实际替代在别处 Ready

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	res, err := r.Reconcile(context.Background(), key())
	if err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseWorkloadEvacuated || got.Status.Reason != "workload-evacuated" {
		t.Fatalf("status = %s/%s，期望 WorkloadEvacuated/workload-evacuated", got.Status.Phase, got.Status.Reason)
	}
	if len(ev.Calls()) != 0 {
		t.Fatal("旧 UID 已消失不重发旧目标")
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatal("终态不再重排队")
	}
}

// TestDrain_NotCompleteWhenReplacementNotReady：旧身份已消失、替代未 Ready——
// WaitingForReplacement，不报完成。
func TestDrain_NotCompleteWhenReplacementNotReady(t *testing.T) {
	dep := ownedDeployment(1, 0) // desired 1，ready 0：替代还没就绪
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep)

	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseWaitingForReplacement || got.Status.Reason != "replacement-not-ready" {
		t.Fatalf("status = %s/%s，期望 WaitingForReplacement/replacement-not-ready", got.Status.Phase, got.Status.Reason)
	}
}

// TestDrain_NotCompleteWhenDesiredZero：desired=0 不是"疏散完成"——交人工确认意图。
func TestDrain_NotCompleteWhenDesiredZero(t *testing.T) {
	dep := ownedDeployment(0, 0)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep)

	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseBlocked || got.Status.Reason != "target-scaled-to-zero" {
		t.Fatalf("status = %s/%s，期望 Blocked/target-scaled-to-zero（副本 0 不当成功）", got.Status.Phase, got.Status.Reason)
	}
}

// TestDrain_UnsupportedPodBlocks：目标节点出现裸 Pod——不声称完整疏散，交人工。
func TestDrain_UnsupportedPodBlocks(t *testing.T) {
	dep := ownedDeployment(1, 1)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	bare := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "maintenance-lab", Name: "stray-job", UID: types.UID("uid-stray")}, // 无 owner
		Spec: corev1.PodSpec{NodeName: "worker-a"}}
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, bare)

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseBlocked || got.Status.Reason != "bare-pod-present" {
		t.Fatalf("status = %s/%s，期望 Blocked/bare-pod-present", got.Status.Phase, got.Status.Reason)
	}
	if len(ev.Calls()) != 0 {
		t.Fatal("出现保留规则外的对象时不驱逐任何 Pod")
	}
}

// TestClassifyPod：保留规则与阻塞分类的纯函数矩阵。
func TestClassifyPod(t *testing.T) {
	cases := []struct {
		name      string
		pod       PodFact
		supported bool
		blocker   string
	}{
		{"目标 Deployment 的干净 Pod", PodFact{ControllerKind: "ReplicaSet", OwnerDeploymentUID: depUID}, true, ""},
		{"目标 Deployment 但挂本地卷", PodFact{ControllerKind: "ReplicaSet", OwnerDeploymentUID: depUID, LocalVolumes: true}, false, "target-pod-local-volumes"},
		{"DaemonSet Pod：保留不驱逐", PodFact{ControllerKind: "DaemonSet"}, false, ""},
		{"静态镜像 Pod：保留不驱逐", PodFact{Mirror: true}, false, ""},
		{"裸 Pod：阻塞", PodFact{}, false, "bare-pod-present"},
		{"StatefulSet Pod：阻塞", PodFact{ControllerKind: "StatefulSet"}, false, "statefulset-pod-present"},
		{"归属不明：阻塞", PodFact{ControllerKind: "ReplicaSet", OwnerDeploymentUID: ""}, false, "unknown-ownership"},
		{"其他业务：阻塞", PodFact{ControllerKind: "ReplicaSet", OwnerDeploymentUID: "uid-other-dep"}, false, "foreign-workload-present"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, b := ClassifyPod(tc.pod, depUID)
			if s != tc.supported || b != tc.blocker {
				t.Fatalf("ClassifyPod = (%v, %q)，期望 (%v, %q)", s, b, tc.supported, tc.blocker)
			}
		})
	}
}

// ---- 恢复（跨进程接续在途记录）----

// TestRecover_RestartDoesNotReEvictGoneOldUID（恢复测试①：不重发旧目标）——TODO(step-4)：
// 由你来写。现场：进度里 activePod 记着旧 UID（受理后响应丢失的真人版离线版），
// 重启（= 新一轮 Reconcile）时旧 UID 已消失、替代已在别处 Ready。
// 断言至少：不发出任何 EvictPod（不重发旧目标、不误驱逐替代 Pod）、status 确认完成事实。
func TestRecover_RestartDoesNotReEvictGoneOldUID(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)

	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	plan.Status.EvictionProgress.ActivePod = &ActivePodRef{
		Namespace: "maintenance-lab",
		Name:      "sentinel-old",
		UID:       "uid-old",
	} // 旧 UID
	resplacement := ownedPod(rs, "sentinel-new", "uid-new", "worker-b", true, false) // 替代在别处 Ready
	c := newTestClient(
		plan,
		cordonedNode("uid-plan-1"),
		dep, rs,
		resplacement,
	)

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	if len(ev.Calls()) != 0 {
		t.Fatalf("旧 UID 已消失不应重发驱逐，got %d 次", len(ev.Calls()))
	}
	got := getPlan(t, c)
	if got.Status.Phase != PhaseWorkloadEvacuated {
		t.Fatalf("status = %s，期望 WorkloadEvacuated", got.Status.Phase)
	}
	if got.Status.Reason != "workload-evacuated" {
		t.Fatalf("reason = %s，期望 workload-evacuated", got.Status.Reason)
	}
}

// TestRecover_DeadlineNotReset（恢复测试②：截止不重置）——TODO(step-4)：
// 由你来写。现场：进度的 deadline 已在过去、目标节点仍有受支持 Pod；
// 重启（新一轮 Reconcile）后断言：停止新驱逐（零调用、Blocked/drain-timeout-exceeded），
// 且 status 里的 startedAt/deadline 与现场一致（没有因重启被刷新）。
// 时间比较注意：替身存储会削去单调时钟与亚秒精度，按秒比较。
func TestRecover_DeadlineNotReset(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)

	plan := cordonedDrainPlan("uid-plan-1", -30*time.Second) // 截止已过
	plan.Status.EvictionProgress.ActivePod = &ActivePodRef{
		Namespace: "maintenance-lab",
		Name:      "sentinel-aaa",
		UID:       "uid-pod-a",
	}

	c := newTestClient(
		plan,
		cordonedNode("uid-plan-1"),
		dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-a", "worker-a", true, false),
	)
	ev := NewFakeEvictor()

	wantStarted := plan.Status.EvictionProgress.StartedAt.Time.Unix()
	wantDeadline := plan.Status.EvictionProgress.Deadline.Time.Unix()

	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}

	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	if len(ev.Calls()) != 0 {
		t.Fatalf("截止已过不得发新驱逐，got %d 次", len(ev.Calls()))
	}
	got := getPlan(t, c)

	if got.Status.Phase != PhaseBlocked {
		t.Fatalf("status = %s，期望 Blocked", got.Status.Phase)
	}
	if got.Status.Reason != "drain-timeout-exceeded" {
		t.Fatalf("reason = %s，期望 drain-timeout-exceeded", got.Status.Reason)
	}
	if got.Status.EvictionProgress == nil {
		t.Fatal("恢复后疏散进度不应丢失")
	}
	if got.Status.EvictionProgress.StartedAt.Time.Unix() != wantStarted {
		t.Fatalf("startedAt = %v，期望 %v", got.Status.EvictionProgress.StartedAt.Time.Unix(), wantStarted)
	}
	if got.Status.EvictionProgress.Deadline.Time.Unix() != wantDeadline {
		t.Fatalf("deadline = %v，期望 %v", got.Status.EvictionProgress.Deadline.Time.Unix(), wantDeadline)
	}
}

// TestRecover_NonTerminatingRetriesSameUID：旧 UID 仍在且非 Terminating——
// 完整前置通过后重发的是同一 UID 绑定请求，不换目标。
func TestRecover_NonTerminatingRetriesSameUID(t *testing.T) {
	dep := ownedDeployment(1, 1)
	rs := rsFor(dep)
	plan := cordonedDrainPlan("uid-plan-1", 10*time.Minute)
	plan.Status.EvictionProgress.ActivePod = &ActivePodRef{
		Namespace: "maintenance-lab", Name: "sentinel-aaa", UID: "uid-pod-a"}
	c := newTestClient(plan, cordonedNode("uid-plan-1"), dep, rs,
		ownedPod(rs, "sentinel-aaa", "uid-pod-a", "worker-a", true, false)) // 仍在且非 Terminating

	ev := NewFakeEvictor()
	r := &MaintenancePlanReconciler{Client: c, Evictor: ev}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	calls := ev.Calls()
	if len(calls) != 1 {
		t.Fatalf("应在完整前置后重试一次，got %d 次", len(calls))
	}
	if calls[0].UID != "uid-pod-a" {
		t.Fatalf("重发必须仍是 UID 绑定的旧目标，got %q", calls[0].UID)
	}
	got := getPlan(t, c)
	if got.Status.Reason != "eviction-request-accepted" {
		t.Fatalf("reason = %s，期望 eviction-request-accepted", got.Status.Reason)
	}
}

// ---- 测试助手 ----

func clientKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: "maintenance-lab", Name: name}
}

// retryHintError：非 PDB 文本的 429＋Retry-After 提示（来源不明的限流形状）。
func retryHintError(seconds int32) *apierrors.StatusError {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    429,
		Reason:  metav1.StatusReasonTooManyRequests,
		Message: "too many requests",
		Details: &metav1.StatusDetails{RetryAfterSeconds: seconds},
	}}
}

// pdbRefusalError：官方 PDB 拒绝消息的 429（预算不足分支不带提示；
// retryAfterSeconds>0 模拟"尚未观察到新代次"分支带 10 秒提示的形状）。
func pdbRefusalError(retryAfterSeconds int32) *apierrors.StatusError {
	st := metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    429,
		Reason:  metav1.StatusReasonTooManyRequests,
		Message: "Cannot evict pod as it would violate the pod's disruption budget.",
	}
	if retryAfterSeconds > 0 {
		st.Details = &metav1.StatusDetails{RetryAfterSeconds: retryAfterSeconds}
	}
	return &apierrors.StatusError{ErrStatus: st}
}

// overlapPdbError：已知重叠 PDB 配置错误的 500（锁定版本的结构化消息原文）。
func overlapPdbError() *apierrors.StatusError {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    500,
		Reason:  metav1.StatusReasonInternalError,
		Message: "This pod has more than one PodDisruptionBudget, which the eviction subresource does not support.",
	}}
}

type errString string

func (e errString) Error() string { return string(e) }
