package main

import "fmt"

// analyze.go：0017 的实现区。把本文件与 analyze_test.go 放进你的 maintenance-preflight 模块
//（0016 starter 所在目录），测试全绿即验收达成。
//
// 两件必要的样板已给全：types.go（0016 带）与本文件底部的 worst / matchLabels。
// 你要做的是把 Analyze 的规则一条条写出来——顺序建议按 0015 的规则表，
// 但只要行为契约满足（测试过），内部组织是你的自由。

// Analyze：纯判断。输入 Snapshot（只读事实），输出 Report（结论 + 原因列表）。
// 约束：不做 API 调用、不看时钟（时间戳取 Snapshot.TakenAt）；
// 同样的输入必须得到同样的输出——这是"可以用字面量直接测"的前提。
//
// 规则清单（行为契约，详见 0015 规则表）：
//
//	R0 读取失败（Errors 非空）        → Unknown，每条失败一个原因，随后早退
//	R1 节点 NotReady                  → Blocked（node-not-ready）
//	R2 目标副本 就绪 < 期望            → Blocked（target-not-ready）
//	R3 节点已 cordon                  → NeedsReview（node-already-cordoned）
//	R4 裸 Pod / 其他控制器负载        → NeedsReview（bare-pod / other-workload）
//	R5 emptyDir/hostPath / PVC        → NeedsReview（local-data / pvc-volume）
//	   DaemonSet / mirror             → 仅记录（skipped-daemonset / skipped-mirror），不改变结论
//	R6 有匹配副本且预算<=0 → Blocked（pdb-budget-zero）
//	   正余量小于本次批次 → NeedsReview（pdb-sequential-review）；逐个推进可恢复预算
//	   同一Pod匹配多个PDB → Blocked（pdb-overlap）
//	   PDB selector 用 matchExpressions → NeedsReview（pdb-selector-unsupported）
//	R7 一条规则都没触发                → ObservedReady（all-rules-passed，原因里写明边界）
//
// 结论优先级：Unknown > Blocked > NeedsReview > ObservedReady；多原因合法。

// worst 取更重的结论（优先级只在这里出现一次）。
func worst(a, b Verdict) Verdict {
	rank := map[Verdict]int{
		VerdictObservedReady: 0,
		VerdictNeedsReview:   1,
		VerdictBlocked:       2,
		VerdictUnknown:       3,
	}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// matchLabels：selector 的每个键值都出现在 Pod 标签里才算圈中（标签子集匹配）。
func matchLabels(selector, podLabels map[string]string) bool {
	for k, v := range selector {
		got, exists := podLabels[k]
		if !exists || got != v {
			return false
		}
	}
	return true
}

func Analyze(s Snapshot) Report {
	res := Report{SnapshotTakenAt: s.TakenAt, Verdict: VerdictObservedReady}
	// TODO(step-1)：实现 R0——Errors 非空时 Unknown 早退，每条错误一个
	if len(s.Errors) > 0 {
		res.Verdict = worst(res.Verdict, VerdictUnknown)
		//   Reason{Code: "read-error/" + e.Step, Detail: e.Message}。
		for _, err := range s.Errors {
			res.Reasons = append(res.Reasons, Reason{
				Code:   "read-error/" + err.Step,
				Detail: err.Message,
			})
		}
		return res
	}
	// TODO(step-2)：实现 R1～R3（节点健康、目标副本、cordon 状态三条规则）。
	if !s.Node.Ready {
		res.Verdict = worst(res.Verdict, VerdictBlocked)
		res.Reasons = append(res.Reasons, Reason{
			Code:   "node-not-ready",
			Detail: "节点状态为 NotReady，无法安全维护",
		})
	}
	if s.Deployment.ReadyReplicas < s.Deployment.DesiredReplicas {
		res.Verdict = worst(res.Verdict, VerdictBlocked)
		res.Reasons = append(res.Reasons, Reason{
			Code:   "target-not-ready",
			Detail: fmt.Sprintf("目标 Deployment %s/%s 就绪副本 %d < 期望副本 %d，无法安全维护", s.Deployment.Namespace, s.Deployment.Name, s.Deployment.ReadyReplicas, s.Deployment.DesiredReplicas),
		})
	}
	if s.Node.Unschedulable {
		res.Verdict = worst(res.Verdict, VerdictNeedsReview)
		res.Reasons = append(res.Reasons, Reason{
			Code:   "node-already-cordoned",
			Detail: "节点已 cordon，需人工核对是否允许 uncordon",
		})
	}
	// TODO(step-3)：实现 R4/R5——遍历 Pods 按 Class 分组收集，排除类给 NeedsReview，
	//   DaemonSet/mirror 只记信息原因；存储检查跳过 DaemonSet/mirror（节点级服务用 hostPath 是常态）。
	for _, pod := range s.Pods {
		if pod.Class == PodClassDaemonSet || pod.Class == PodClassMirror {
			res.Reasons = append(res.Reasons, Reason{
				Code:   "skipped-" + string(pod.Class),
				Detail: "节点上存在 DaemonSet/mirror Pod，已跳过检查",
			})
			continue
		}
		if pod.UsesPVC {
			res.Verdict = worst(res.Verdict, VerdictNeedsReview)
			res.Reasons = append(res.Reasons, Reason{
				Code:   "pvc-volume",
				Detail: "节点上存在使用 PVC 的 Pod，需人工核对是否允许维护",
			})
		}
		if pod.HasLocalData && pod.Class != PodClassDaemonSet && pod.Class != PodClassMirror {
			res.Verdict = worst(res.Verdict, VerdictNeedsReview)
			res.Reasons = append(res.Reasons, Reason{
				Code:   "local-data",
				Detail: "节点上存在使用 emptyDir/hostPath 的 Pod，需人工核对是否允许维护",
			})
		}
		switch pod.Class {
		case PodClassBare:
			res.Verdict = worst(res.Verdict, VerdictNeedsReview)
			res.Reasons = append(res.Reasons, Reason{
				Code:   "bare-pod",
				Detail: "节点上存在裸 Pod，需人工核对是否允许维护",
			})
		case PodClassOther:
			res.Verdict = worst(res.Verdict, VerdictNeedsReview)
			res.Reasons = append(res.Reasons, Reason{
				Code:   "other-workload",
				Detail: "节点上存在其他控制器管理的 Pod，需人工核对是否允许维护",
			})
		}
		// pdb overlap
		match := 0
		for _, pdb := range s.PDBs {
			if pod.Class != PodClassTarget && pod.Class != PodClassOther {
				continue
			}
			if pdb.SelectorExpr {
				continue
			}
			if matchLabels(pdb.Selector, pod.Labels) && pdb.Namespace == pod.Namespace {
				match++
			}
		}
		if match > 1 {
			res.Verdict = worst(res.Verdict, VerdictBlocked)
			res.Reasons = append(res.Reasons, Reason{
				Code: "pdb-overlap",
				Detail: fmt.Sprintf(
					"Pod %s/%s 匹配多个 PDB，无法安全维护",
					pod.Namespace, pod.Name,
				),
			})
		}
	}
	for _, pdb := range s.PDBs {
		if pdb.SelectorExpr {
			res.Verdict = worst(res.Verdict, VerdictNeedsReview)
			res.Reasons = append(res.Reasons, Reason{
				Code:   "pdb-selector-unsupported",
				Detail: "PDB " + pdb.Namespace + "/" + pdb.Name + " 使用 matchExpressions，需人工核对是否允许维护",
			})
			continue
		}
		//  正余量小于批次量交人工安排顺序
		match := 0
		for _, pod := range s.Pods {
			if matchLabels(pdb.Selector, pod.Labels) && (pod.Class == PodClassTarget || pod.Class == PodClassOther) && pdb.Namespace == pod.Namespace {
				match++
			}
		}
		if match == 0 {
			continue
		}

		// pdb-budget-zero
		if match > 0 && pdb.DisruptionsAllowed <= 0 {
			res.Verdict = worst(res.Verdict, VerdictBlocked)
			res.Reasons = append(res.Reasons, Reason{
				Code:   "pdb-budget-zero",
				Detail: "PDB " + pdb.Namespace + "/" + pdb.Name + " 预算为 0，无法安全维护",
			})
		}
		// pdb-sequential-review
		if match > 0 && pdb.DisruptionsAllowed < int32(match) && pdb.DisruptionsAllowed > 0 {
			res.Verdict = worst(res.Verdict, VerdictNeedsReview)
			res.Reasons = append(res.Reasons, Reason{
				Code:   "pdb-sequential-review",
				Detail: "PDB " + pdb.Namespace + "/" + pdb.Name + " 预算小于匹配 Pod 数量，需人工安排顺序维护",
			})
		}

	}
	// TODO(step-5)：实现 R7——Verdict为ObservedReady时（即使已有DaemonSet信息原因）给 all-rules-passed，Detail 写明 ObservedReady 的边界
	//   （受限观察、非原子读取、Eviction 仍由原生 API 判定）。
	if res.Verdict == VerdictObservedReady {
		res.Reasons = append(res.Reasons, Reason{
			Code:   "all-rules-passed",
			Detail: "受限观察满足规则，非原子读取，Eviction 仍由原生 API 判定",
		})
	}
	return res
}
