package main

// reconciler_test.go：替身层测试——W43 受控 cordon 行为契约（自 0021 移植，
// 语义不变）＋ W44 测试共用的装配助手。同时断言"原因"与"副作用"。

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := AddToScheme(scheme); err != nil {
		panic(err)
	}
	return scheme
}

// newTestClient：W44 需要注册 spec.nodeName 索引（ReadFacts 用 field selector）。
func newTestClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithStatusSubresource(&MaintenancePlan{}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		WithObjects(objs...).
		Build()
}

func testPlan(uid string) *MaintenancePlan {
	return &MaintenancePlan{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupName + "/" + GroupVersion, Kind: "MaintenancePlan"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "maintenance-lab", Name: "plan-1", UID: types.UID(uid), Generation: 1},
		Spec: MaintenancePlanSpec{
			NodeName:  "worker-a",
			TargetRef: TargetRef{Namespace: "maintenance-lab", Name: "sentinel"},
		},
	}
}

func readyNode(name, uid string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)},
		Spec:       corev1.NodeSpec{Unschedulable: false},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}
}

func readyDeployment(namespace, name string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sentinel"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "sentinel"}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
}

func pdbZero(namespace string) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "sentinel-pdb"},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sentinel"}}},
		Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0},
	}
}

func key() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "maintenance-lab", Name: "plan-1"}}
}

func getPlan(t *testing.T, c client.Client) *MaintenancePlan {
	t.Helper()
	var p MaintenancePlan
	if err := c.Get(context.Background(), key().NamespacedName, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func getNode(t *testing.T, c client.Client) *corev1.Node {
	t.Helper()
	var n corev1.Node
	if err := c.Get(context.Background(), client.ObjectKey{Name: "worker-a"}, &n); err != nil {
		t.Fatal(err)
	}
	return &n
}

// ---- W43 移植用例（语义与 0021 一致；构造器多注入一个 fakeEvictor）----

func TestCordon_HappyPath(t *testing.T) {
	c := newTestClient(testPlan("uid-plan-1"), readyNode("worker-a", "uid-node-1"), readyDeployment("maintenance-lab", "sentinel"))
	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}

	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	node := getNode(t, c)
	if !node.Spec.Unschedulable {
		t.Fatal("节点应已被 cordon（unschedulable=true）")
	}
	if got := node.Annotations[MarkerAnnotation]; got != "uid-plan-1" {
		t.Fatalf("marker = %q, 期望 uid-plan-1（同一次更新里写入）", got)
	}
	plan := getPlan(t, c)
	if plan.Status.Phase != PhaseCordoned || plan.Status.Reason != "cordon-applied" {
		t.Fatalf("status = %s/%s, 期望 Cordoned/cordon-applied", plan.Status.Phase, plan.Status.Reason)
	}
	if plan.Status.CordonIntent == nil || plan.Status.CordonIntent.NodeUID != "uid-node-1" {
		t.Fatalf("cordonIntent 缺失或 UID 不符: %+v", plan.Status.CordonIntent)
	}
	if plan.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration = %d, 期望 1", plan.Status.ObservedGeneration)
	}
}

func TestCordon_RepeatedNoSecondWrite(t *testing.T) {
	c := newTestClient(testPlan("uid-plan-1"), readyNode("worker-a", "uid-node-1"), readyDeployment("maintenance-lab", "sentinel"))
	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}

	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	rv1 := getNode(t, c).ResourceVersion

	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	node := getNode(t, c)
	if node.ResourceVersion != rv1 {
		t.Fatalf("重复协调再次写了 Node：rv %s → %s", rv1, node.ResourceVersion)
	}
	if !node.Spec.Unschedulable || node.Annotations[MarkerAnnotation] != "uid-plan-1" {
		t.Fatal("重复协调后节点状态不应变化")
	}
}

func TestCordon_ForeignCordonNotOurs(t *testing.T) {
	n := readyNode("worker-a", "uid-node-1")
	n.Spec.Unschedulable = true // 外部 cordon（无 marker）
	c := newTestClient(testPlan("uid-plan-1"), n, readyDeployment("maintenance-lab", "sentinel"))
	rvBefore := n.ResourceVersion

	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	node := getNode(t, c)
	if node.ResourceVersion != rvBefore {
		t.Fatal("外部 cordon 不应被接管（Node 不该被写）")
	}
	if node.Annotations[MarkerAnnotation] != "" {
		t.Fatal("不应给别人的 cordon 补上自己的标记")
	}
	plan := getPlan(t, c)
	if plan.Status.Phase != PhaseBlocked || plan.Status.Reason != "node-already-cordoned" {
		t.Fatalf("status = %s/%s, 期望 Blocked/node-already-cordoned", plan.Status.Phase, plan.Status.Reason)
	}
}

func TestCordon_ForeignMarker(t *testing.T) {
	n := readyNode("worker-a", "uid-node-1")
	n.Annotations = map[string]string{MarkerAnnotation: "uid-someone-else"}
	c := newTestClient(testPlan("uid-plan-1"), n, readyDeployment("maintenance-lab", "sentinel"))

	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	node := getNode(t, c)
	if node.Spec.Unschedulable {
		t.Fatal("有他人标记时不应 cordon")
	}
	plan := getPlan(t, c)
	if plan.Status.Phase != PhaseBlocked || plan.Status.Reason != "foreign-marker" {
		t.Fatalf("status = %s/%s, 期望 Blocked/foreign-marker", plan.Status.Phase, plan.Status.Reason)
	}
}

func TestCordon_PdbZeroBlocks(t *testing.T) {
	c := newTestClient(testPlan("uid-plan-1"), readyNode("worker-a", "uid-node-1"),
		readyDeployment("maintenance-lab", "sentinel"), pdbZero("maintenance-lab"))
	rvBefore := getNode(t, c).ResourceVersion

	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	if getNode(t, c).ResourceVersion != rvBefore {
		t.Fatal("PDB 阻塞时不应写 Node")
	}
	plan := getPlan(t, c)
	if plan.Status.Phase != PhaseBlocked || plan.Status.Reason != "pdb-budget-zero" {
		t.Fatalf("status = %s/%s, 期望 Blocked/pdb-budget-zero", plan.Status.Phase, plan.Status.Reason)
	}
}

func TestCordon_TargetUIDChanged(t *testing.T) {
	p := testPlan("uid-plan-1")
	p.Status.CordonIntent = &CordonIntent{NodeUID: "uid-node-OLD"}
	c := newTestClient(p, readyNode("worker-a", "uid-node-NEW"), readyDeployment("maintenance-lab", "sentinel"))
	rvBefore := getNode(t, c).ResourceVersion

	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	if getNode(t, c).ResourceVersion != rvBefore {
		t.Fatal("目标 UID 变化时应交人工，不写 Node")
	}
	plan := getPlan(t, c)
	if plan.Status.Reason != "target-uid-changed" {
		t.Fatalf("reason = %s, 期望 target-uid-changed", plan.Status.Reason)
	}
}

// TestCordon_UIDChangedOwnMarkerNotClaimed：组合反例——意图记着旧 UID，同名新 Node
// 却带着本申请的 marker 且已 cordon。认领必须先过身份门：交人工，Node 原样不动。
func TestCordon_UIDChangedOwnMarkerNotClaimed(t *testing.T) {
	p := testPlan("uid-plan-1")
	p.Status.CordonIntent = &CordonIntent{NodeUID: "uid-node-OLD"} // 上次核对过的身份
	n := readyNode("worker-a", "uid-node-NEW")                     // 同名新对象
	n.Spec.Unschedulable = true
	n.Annotations = map[string]string{MarkerAnnotation: "uid-plan-1"}
	c := newTestClient(p, n, readyDeployment("maintenance-lab", "sentinel"))
	rvBefore := getNode(t, c).ResourceVersion

	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatal(err)
	}
	node := getNode(t, c)
	if node.ResourceVersion != rvBefore {
		t.Fatal("身份对不上时不应写 Node")
	}
	if got := node.Annotations[MarkerAnnotation]; got != "uid-plan-1" {
		t.Fatalf("存疑的标记应原样保留（无权清除他人或来源不明的标记），got %q", got)
	}
	plan := getPlan(t, c)
	if plan.Status.Phase == PhaseCordoned || plan.Status.Reason != "target-uid-changed" {
		t.Fatalf("status = %s/%s, 期望 Blocked/target-uid-changed（组合反例不得认领）", plan.Status.Phase, plan.Status.Reason)
	}
}

// TestCordon_RereadPreconditionsChanged：Decide 与提交之间隔了一次重读——
// 拦截器在"执行段重读"那一刻插一脚，模拟三种外部变化；三种都不得提交、不得报成功、应重排队重判。
func TestCordon_RereadPreconditionsChanged(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(n *corev1.Node)
	}{
		{"同名新对象（UID 变）", func(n *corev1.Node) { n.UID = types.UID("uid-node-REPLACED") }},
		{"外部 cordon 出现", func(n *corev1.Node) { n.Spec.Unschedulable = true }},
		{"他人 marker 出现", func(n *corev1.Node) {
			n.Annotations = map[string]string{MarkerAnnotation: "uid-someone-else"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeGets := 0
			c := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithStatusSubresource(&MaintenancePlan{}).
				WithObjects(testPlan("uid-plan-1"), readyNode("worker-a", "uid-node-1"),
					readyDeployment("maintenance-lab", "sentinel")).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
					return []string{o.(*corev1.Pod).Spec.NodeName}
				}).
				WithInterceptorFuncs(interceptor.Funcs{
					// 第 1 次读 Node 是 ReadFacts，第 2 次是执行段重读——只改第 2 次的返回值。
					Get: func(ctx context.Context, dc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if err := dc.Get(ctx, key, obj, opts...); err != nil {
							return err
						}
						if n, ok := obj.(*corev1.Node); ok && n.Name == "worker-a" {
							nodeGets++
							if nodeGets == 2 {
								tc.mutate(n)
							}
						}
						return nil
					},
				}).
				Build()

			r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
			res, err := r.Reconcile(context.Background(), key())
			if err != nil {
				t.Fatal(err)
			}
			if !res.Requeue && res.RequeueAfter == 0 {
				t.Fatal("放弃提交后应重排队，下一轮带新事实重新判断")
			}
			node := getNode(t, c)
			if node.Spec.Unschedulable || node.Annotations[MarkerAnnotation] != "" {
				t.Fatal("重读后前置变化，本轮不得提交（Node 不应被写）")
			}
			plan := getPlan(t, c)
			if plan.Status.Phase == PhaseCordoned {
				t.Fatal("没有提交成功就不得报 Cordoned——失败不报成功")
			}
		})
	}
}

func TestReconcile_PlanNotFound(t *testing.T) {
	r := &MaintenancePlanReconciler{Client: newTestClient(), Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err != nil {
		t.Fatalf("NotFound 应正常结束: %v", err)
	}
}

func TestReconcile_FactsMissingIsError(t *testing.T) {
	c := newTestClient(testPlan("uid-plan-1")) // 没有节点与 Deployment
	r := &MaintenancePlanReconciler{Client: c, Evictor: NewFakeEvictor()}
	if _, err := r.Reconcile(context.Background(), key()); err == nil {
		t.Fatal("事实缺失应返回错误")
	}
}

// ---- W44 装配助手 ----

const (
	nodeUID = "uid-node-1"
	depUID  = "uid-dep-1"
)

// ownedDeployment：带 UID 与代次事实的目标 Deployment（W44 进度绑定的身份）。
func ownedDeployment(desired, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "maintenance-lab", Name: "sentinel", UID: types.UID(depUID), Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desired,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sentinel"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "sentinel"}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready, ObservedGeneration: 1},
	}
}

func rsFor(dep *appsv1.Deployment) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "maintenance-lab", Name: "sentinel-rs", UID: "uid-rs-1",
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(dep, appsv1.SchemeGroupVersion.WithKind("Deployment")),
			},
		},
	}
}

// ownedPod：归属链完整的受支持业务 Pod（Pod→RS→Deployment 逐层 UID 可核对）。
func ownedPod(rs *appsv1.ReplicaSet, name, uid, nodeName string, ready, terminating bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "maintenance-lab", Name: name, UID: types.UID(uid),
			Labels: map[string]string{"app": "sentinel"},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet")),
			},
		},
		Spec: corev1.PodSpec{NodeName: nodeName},
	}
	if ready {
		p.Status.Conditions = append(p.Status.Conditions,
			corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
	}
	if terminating {
		now := metav1.Now()
		p.DeletionTimestamp = &now
		// fake client 不接受"有 deletionTimestamp 却无 finalizer"的对象；
		// 真实世界里 Terminating 的 Pod 也可以带着 finalizer（如cilium/kubelet 的）。
		p.Finalizers = []string{"maintenance.studymate.io/test-terminating"}
	}
	return p
}

func cordonedNode(planUID string) *corev1.Node {
	n := readyNode("worker-a", nodeUID)
	n.Spec.Unschedulable = true
	n.Annotations = map[string]string{MarkerAnnotation: planUID}
	return n
}

// cordonedDrainPlan：已过 cordon、进度已保存的申请（deadlineIn：距现在的截止余量）。
func cordonedDrainPlan(uid string, deadlineIn time.Duration) *MaintenancePlan {
	plan := testPlan(uid)
	plan.Spec.AllowEviction = true
	plan.Status.Phase = PhaseCordoned
	plan.Status.Reason = "cordon-already-applied"
	plan.Status.CordonIntent = &CordonIntent{NodeUID: nodeUID}
	plan.Status.ObservedGeneration = 1
	now := time.Now()
	plan.Status.EvictionProgress = &EvictionProgress{
		PlanGeneration: 1,
		DeploymentUID:  depUID,
		StartedAt:      metav1.NewTime(now.Add(-time.Minute)),
		Deadline:       metav1.NewTime(now.Add(deadlineIn)),
	}
	return plan
}
