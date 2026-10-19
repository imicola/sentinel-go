package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 这套测试就是 0017 的验收清单：七个必测场景 + 多原因 + JSON 输出。
// 期望全部来自 0015 定下的行为契约（规则表），不是照抄 Analyze 的实现。
// 用法：把本文件与 analyze.go 放进你的 maintenance-preflight 模块，go test ./...

// baseSnapshot：一个"正常受限观察"的底稿——两台 worker 里被维护那台（worker-a）上，
// 目标副本就绪、节点健康可调度、节点级服务是 DaemonSet、没有 PDB。各用例按需改字段。
func baseSnapshot() Snapshot {
	return Snapshot{
		TakenAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC),
		Node:    NodeState{Name: "work-maintenance-worker", Ready: true, Unschedulable: false},
		Deployment: DeploymentState{
			Namespace: "maintenance-lab", Name: "maintenance-sentinel",
			DesiredReplicas: 1, ReadyReplicas: 1,
		},
		Pods: []PodState{
			{Namespace: "maintenance-lab", Name: "maintenance-sentinel-7b6c8d9e5f-q8v3n",
				Class: PodClassTarget, Ready: true,
				Labels:    map[string]string{"app": "maintenance-sentinel"},
				OwnerKind: "ReplicaSet", OwnerName: "maintenance-sentinel-7b6c8d9e5f"},
			{Namespace: "kube-system", Name: "kube-proxy-d7vkl",
				Class: PodClassDaemonSet, Ready: true, OwnerKind: "DaemonSet", OwnerName: "kube-proxy"},
			{Namespace: "kube-system", Name: "kindnet-c8s4h",
				Class: PodClassDaemonSet, Ready: true, OwnerKind: "DaemonSet", OwnerName: "kindnet"},
		},
	}
}

func codes(r Report) []string {
	out := make([]string, 0, len(r.Reasons))
	for _, x := range r.Reasons {
		out = append(out, x.Code)
	}
	return out
}

func hasCode(r Report, want string) bool {
	for _, c := range codes(r) {
		if c == want {
			return true
		}
	}
	return false
}

// 场景 1：正常受限观察 → ObservedReady；DaemonSet 记录为 skipped，不阻塞。
func TestAnalyze_NormalCase_ObservedReady(t *testing.T) {
	r := Analyze(baseSnapshot())
	if r.Verdict != VerdictObservedReady {
		t.Fatalf("结论 = %s，期望 ObservedReady（原因：%v）", r.Verdict, codes(r))
	}
	if !hasCode(r, "skipped-daemonset") {
		t.Fatalf("DaemonSet 应记录为 skipped-daemonset，实际原因：%v", codes(r))
	}
	for _, bad := range []string{"node-not-ready", "target-not-ready", "pdb-budget-zero", "bare-pod"} {
		if hasCode(r, bad) {
			t.Fatalf("正常场景不应出现 %s", bad)
		}
	}
}

// 场景 2：Node 未 Ready → Blocked。
func TestAnalyze_NodeNotReady_Blocked(t *testing.T) {
	s := baseSnapshot()
	s.Node.Ready = false
	r := Analyze(s)
	if r.Verdict != VerdictBlocked {
		t.Fatalf("结论 = %s，期望 Blocked", r.Verdict)
	}
	if !hasCode(r, "node-not-ready") {
		t.Fatalf("缺少 node-not-ready，实际原因：%v", codes(r))
	}
}

// 场景 3：PDB 预算 0 且圈中将被驱逐的副本 → Blocked。
func TestAnalyze_PDBBudgetZero_Blocked(t *testing.T) {
	s := baseSnapshot()
	s.PDBs = []PDBState{{
		Namespace: "maintenance-lab", Name: "maintenance-pdb",
		CurrentHealthy: 1, DesiredHealthy: 1, DisruptionsAllowed: 0,
		Selector: map[string]string{"app": "maintenance-sentinel"},
	}}
	r := Analyze(s)
	if r.Verdict != VerdictBlocked {
		t.Fatalf("结论 = %s，期望 Blocked", r.Verdict)
	}
	if !hasCode(r, "pdb-budget-zero") {
		t.Fatalf("缺少 pdb-budget-zero，实际原因：%v", codes(r))
	}
}

// 场景 3 对照：同样一份 PDB，副本补足后预算为 1 → 不再阻塞（回到 ObservedReady）。
func TestAnalyze_PDBBudgetRestored_NotBlocked(t *testing.T) {
	s := baseSnapshot()
	s.PDBs = []PDBState{{
		Namespace: "maintenance-lab", Name: "maintenance-pdb",
		CurrentHealthy: 2, DesiredHealthy: 1, DisruptionsAllowed: 1,
		Selector: map[string]string{"app": "maintenance-sentinel"},
	}}
	r := Analyze(s)
	if r.Verdict == VerdictBlocked {
		t.Fatalf("预算恢复后不应 Blocked，原因：%v", codes(r))
	}
}

// 场景 4：目标副本未 Ready → Blocked。
func TestAnalyze_TargetNotReady_Blocked(t *testing.T) {
	s := baseSnapshot()
	s.Deployment.DesiredReplicas = 2
	s.Deployment.ReadyReplicas = 1
	r := Analyze(s)
	if r.Verdict != VerdictBlocked {
		t.Fatalf("结论 = %s，期望 Blocked", r.Verdict)
	}
	if !hasCode(r, "target-not-ready") {
		t.Fatalf("缺少 target-not-ready，实际原因：%v", codes(r))
	}
}

// 场景 5：未知/排除对象（裸 Pod、其他控制器、emptyDir、PVC）→ NeedsReview。
func TestAnalyze_ExcludedPods_NeedsReview(t *testing.T) {
	s := baseSnapshot()
	s.Pods = append(s.Pods,
		PodState{Namespace: "default", Name: "debug-shell", Class: PodClassBare},
		PodState{Namespace: "other-lab", Name: "worker-job-pod", Class: PodClassOther, OwnerKind: "Job"},
		PodState{Namespace: "maintenance-lab", Name: "target-with-emptydir", Class: PodClassTarget, HasLocalData: true},
		PodState{Namespace: "other-lab", Name: "db-0", Class: PodClassOther, UsesPVC: true},
	)
	r := Analyze(s)
	if r.Verdict != VerdictNeedsReview {
		t.Fatalf("结论 = %s，期望 NeedsReview（原因：%v）", r.Verdict, codes(r))
	}
	for _, want := range []string{"bare-pod", "other-workload", "local-data", "pvc-volume"} {
		if !hasCode(r, want) {
			t.Fatalf("缺少 %s，实际原因：%v", want, codes(r))
		}
	}
}

// 场景 6：节点原本已 cordon → NeedsReview（核对所有权，不自动 uncordon）。
func TestAnalyze_NodeAlreadyCordoned_NeedsReview(t *testing.T) {
	s := baseSnapshot()
	s.Node.Unschedulable = true
	r := Analyze(s)
	if r.Verdict != VerdictNeedsReview {
		t.Fatalf("结论 = %s，期望 NeedsReview", r.Verdict)
	}
	if !hasCode(r, "node-already-cordoned") {
		t.Fatalf("缺少 node-already-cordoned，实际原因：%v", codes(r))
	}
}

// 场景 7：读取失败 → Unknown，且不允许伪装成正常（也不在零值上产出误导性原因）。
func TestAnalyze_ReadErrors_UnknownNotNormal(t *testing.T) {
	s := baseSnapshot()
	s.Node = NodeState{} // 模拟 read-node 失败留下的零值
	s.Errors = []ReadError{
		{Step: "read-node", Message: "nodes \"work-maintenance-worker\" not found"},
	}
	r := Analyze(s)
	if r.Verdict != VerdictUnknown {
		t.Fatalf("结论 = %s，期望 Unknown", r.Verdict)
	}
	if !hasCode(r, "read-error/read-node") {
		t.Fatalf("缺少 read-error/read-node，实际原因：%v", codes(r))
	}
	if hasCode(r, "node-not-ready") {
		t.Fatalf("读取失败时不得用零值跑出 node-not-ready 这种误导原因")
	}
}

// 契约：多原因合法——两个 Blocked 条件同时存在时，两个原因都报，结论取更重的。
func TestAnalyze_MultipleReasons_HeaviestWins(t *testing.T) {
	s := baseSnapshot()
	s.Node.Ready = false
	s.Deployment.DesiredReplicas = 2
	s.Deployment.ReadyReplicas = 1
	s.Node.Unschedulable = true
	r := Analyze(s)
	if r.Verdict != VerdictBlocked {
		t.Fatalf("结论 = %s，期望 Blocked（Blocked 比 NeedsReview 重）", r.Verdict)
	}
	for _, want := range []string{"node-not-ready", "target-not-ready", "node-already-cordoned"} {
		if !hasCode(r, want) {
			t.Fatalf("缺少 %s，实际原因：%v", want, codes(r))
		}
	}
}

// 契约：PDB selector 用 matchExpressions → 不解析、保守 NeedsReview。
func TestAnalyze_PDBSelectorExpr_NeedsReview(t *testing.T) {
	s := baseSnapshot()
	s.PDBs = []PDBState{{
		Namespace: "maintenance-lab", Name: "expr-pdb",
		DisruptionsAllowed: 1, SelectorExpr: true,
	}}
	r := Analyze(s)
	if r.Verdict != VerdictNeedsReview {
		t.Fatalf("结论 = %s，期望 NeedsReview", r.Verdict)
	}
	if !hasCode(r, "pdb-selector-unsupported") {
		t.Fatalf("缺少 pdb-selector-unsupported，实际原因：%v", codes(r))
	}
}

// 输出契约：Report 序列化后必须带观察时间与结论（这是"带原因和观察时间"的最低要求）。
func TestReport_JSONCarriesTimeAndVerdict(t *testing.T) {
	r := Analyze(baseSnapshot())
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"Verdict":"ObservedReady"`) {
		t.Fatalf("JSON 缺少 Verdict 字段或值不对: %s", out)
	}
	if !strings.Contains(out, "SnapshotTakenAt") {
		t.Fatalf("JSON 缺少 SnapshotTakenAt 字段: %s", out)
	}
}

func TestAnalyze_PositiveBudgetRequiresSequentialReview(t *testing.T) {
	s := baseSnapshot()
	s.Pods = append(s.Pods, s.Pods[0])
	s.PDBs = []PDBState{{Namespace: "maintenance-lab", Name: "pdb", Selector: map[string]string{"app": "maintenance-sentinel"}, DisruptionsAllowed: 1}}
	r := Analyze(s)
	if r.Verdict != VerdictNeedsReview || !hasCode(r, "pdb-sequential-review") || hasCode(r, "pdb-budget-zero") {
		t.Fatalf("正余量不等于零预算: %+v", r)
	}
}
func TestAnalyze_OverlappingPDBsBlocked(t *testing.T) {
	s := baseSnapshot()
	p := PDBState{Namespace: "maintenance-lab", Name: "one", Selector: map[string]string{"app": "maintenance-sentinel"}, DisruptionsAllowed: 1}
	s.PDBs = []PDBState{p, p}
	s.PDBs[1].Name = "two"
	r := Analyze(s)
	if r.Verdict != VerdictBlocked || !hasCode(r, "pdb-overlap") {
		t.Fatalf("重叠PDB不可通过: %+v", r)
	}
}
func TestAnalyze_ReadyWithDaemonSetCarriesBoundary(t *testing.T) {
	r := Analyze(baseSnapshot())
	if !hasCode(r, "all-rules-passed") {
		t.Fatalf("正常报告必须带边界: %+v", r)
	}
}

func TestAnalyze_PVCDoesNotHidePDBOverlap(t *testing.T) {
	s := baseSnapshot()
	s.Pods = append(s.Pods, PodState{Namespace: "maintenance-lab", Name: "pvc-pod", Class: PodClassOther, UsesPVC: true, Labels: map[string]string{"case": "pvc-overlap"}})
	p := PDBState{Namespace: "maintenance-lab", Name: "pdb1", Selector: map[string]string{"case": "pvc-overlap"}, DisruptionsAllowed: 1}
	s.PDBs = []PDBState{p, p}
	s.PDBs[1].Name = "pdb2"
	r := Analyze(s)
	if r.Verdict != VerdictBlocked || !hasCode(r, "pdb-overlap") {
		t.Fatalf("PVC 不应掩盖 PDB 重叠: %+v", r)
	}
	if !hasCode(r, "pvc-volume") {
		t.Fatalf("PVC 仍应被报告: %+v", r)
	}
}
