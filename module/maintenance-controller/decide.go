package main

// decide.go：判断面——纯函数，同样的输入永远同样的输出。
// W43 部分与 0021 完全一致（身份门→认领→外部cordon→他人标记→预检→cordon）。
// W44 新增疏散阶段判断 DecideDrain 与受限完成判断 evacuationComplete：
// 判断次序即优先级——先身份，再"已完成事实"，再截止，再在途，最后选下一个目标。

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Action：本轮选择的动作。
type Action string

const (
	ActionNone   Action = "none"
	ActionHold   Action = "hold"
	ActionCordon Action = "cordon"
	// ActionEvict：W44——本轮至多一次的 UID 绑定驱逐请求。
	ActionEvict Action = "evict"
)

// Decision：W43 判断输出。
type Decision struct {
	Action  Action
	Phase   Phase
	Reason  string
	Requeue bool
}

// Decide：观察型判断（含身份认领）。W43 语义原样保留。
func Decide(plan *MaintenancePlan, f Facts) Decision {
	if plan.Status.CordonIntent != nil && plan.Status.CordonIntent.NodeUID != "" &&
		plan.Status.CordonIntent.NodeUID != f.NodeUID {
		return Decision{Action: ActionHold, Phase: PhaseBlocked, Reason: "target-uid-changed"}
	}
	if f.MarkerPlanUID == string(plan.UID) && f.NodeUnschedulable {
		return Decision{Action: ActionNone, Phase: PhaseCordoned, Reason: "cordon-already-applied"}
	}
	if f.NodeUnschedulable {
		return Decision{Action: ActionHold, Phase: PhaseBlocked, Reason: "node-already-cordoned"}
	}
	if f.MarkerPlanUID != "" {
		return Decision{Action: ActionHold, Phase: PhaseBlocked, Reason: "foreign-marker", Requeue: true}
	}
	if !f.NodeReady {
		return Decision{Action: ActionHold, Phase: PhaseBlocked, Reason: "node-not-ready", Requeue: true}
	}
	if f.TargetReady < f.TargetDesired {
		return Decision{Action: ActionHold, Phase: PhaseBlocked, Reason: "target-not-ready", Requeue: true}
	}
	if f.PdbMatched && f.PdbBudget <= 0 {
		return Decision{Action: ActionHold, Phase: PhaseBlocked, Reason: "pdb-budget-zero", Requeue: true}
	}
	return Decision{Action: ActionCordon, Phase: PhasePending, Reason: "preflight-clear", Requeue: true}
}

// DrainDecision：疏散阶段判断输出。
type DrainDecision struct {
	Action       Action
	Phase        Phase
	Reason       string
	Requeue      bool
	RequeueAfter time.Duration // 有界重排队；0 → 用默认间隔

	// Evict：Action==evict 时的本轮唯一目标。调用前协调层还要现读核对。
	Evict *ActivePodRef
	// ClearActive：在途记录需要清空（旧身份已消失/同名换了对象）。
	ClearActive bool
}

// DecideDrain：疏散阶段判断。只在本申请已完成受控 cordon（进度存在）后被调用；
// cordonIntent 身份门由此处的 Node 身份复核承担。
//
// 门次序（每条都有 W44 机制合同出处，见课文）：
//  1. 处理期间 spec generation 变化 → 交人工（不默默迁移原进度）；
//  2. 目标 Deployment UID 变化 → 交人工；
//  3. Node 身份/归属复核：UID 仍是意图记的、仍不可调度、marker 仍是本申请；
//  4. 完成判据——"已完成事实"的确认先于截止（预算下降/截止到了都不挡事实）；
//     4.5 已进入人工态的明确配置/权限拒绝（403、已知重叠 PDB 配置错误）：
//     人工处理前不再自动请求；
//  5. 固定截止：超过就停止新驱逐，Blocked 交人工（重启后仍保持）；
//  6. 在途核对：至多一份 activePod——Terminating 等待；仍在且非 Terminating
//     时先按当下事实重新分类（UID 没变不等于归属/支持条件仍成立），仍受
//     支持才重发同一 UID；旧 UID 已消失不重发旧目标；
//  7. 无在途的新目标选择，先做完全部核对再动：全量分类（任何保留规则外的
//     对象先阻塞）→ 已有 Terminating 业务对象只观察 → 替代缺口未填先等待
//     （数实际 Ready 副本，不用 Deployment 计数差）→ 才选名字序第一个受支持
//     Pod；无目标则按完成缺口报告。
func DecideDrain(plan *MaintenancePlan, f *Facts, now time.Time) DrainDecision {
	prog := plan.Status.EvictionProgress

	// 1) 申请自身在处理期间被改写：旧进度绑定的不再是现在的意图。
	if prog.PlanGeneration != plan.Generation {
		return DrainDecision{
			Action: ActionHold,
			Phase:  PhaseBlocked,
			Reason: "plan-spec-changed",
		}
	}
	// 2) 目标 Deployment 换了对象（同名新 UID）：交人工，不默默迁移进度。
	if f.TargetUID != prog.DeploymentUID {
		return DrainDecision{Action: ActionHold, Phase: PhaseBlocked, Reason: "deployment-uid-changed"}
	}
	// 3) Node 身份与 cordon 归属复核（W43 提交复核的疏散版）。
	if plan.Status.CordonIntent == nil || plan.Status.CordonIntent.NodeUID != f.NodeUID {
		return DrainDecision{Action: ActionHold, Phase: PhaseBlocked, Reason: "target-uid-changed"}
	}
	if !f.NodeUnschedulable || f.MarkerPlanUID != string(plan.UID) {
		return DrainDecision{Action: ActionHold, Phase: PhaseBlocked, Reason: "cordon-ownership-lost"}
	}

	// 4) 完成判据：事实先于截止。PDB 预算刻意不参与——驱逐完成后预算下降
	//    是正常结果，不能因此否认"已完成事实"。
	if complete, missing := evacuationComplete(f, prog); complete {
		return DrainDecision{Action: ActionNone, Phase: PhaseWorkloadEvacuated, Reason: "workload-evacuated"}
	} else if missing == "deployment-uid-changed" {
		// evacuationComplete 的身份双检在门 2 之后属于防御分支，保持同一去向。
		return DrainDecision{Action: ActionHold, Phase: PhaseBlocked, Reason: "deployment-uid-changed"}
	}

	// 4.5) 上一轮已进入人工态的明确拒绝（给定）：403（权限）与已知重叠 PDB
	//     配置错误（500 结构化消息，reason=pdb-overlap-misconfigured）——人工
	//     处理前不再自动请求。若事实已构成完成，门 4 已放行；这里只挡"再发新请求"。
	if plan.Status.Phase == PhaseBlocked {
		switch plan.Status.Reason {
		case "eviction-forbidden", "pdb-overlap-misconfigured":
			return DrainDecision{Action: ActionHold, Phase: PhaseBlocked, Reason: plan.Status.Reason}
		}
	}

	// 5) 固定截止：停止新驱逐，交人工。重启后本判断依旧成立（截止持久）。
	if now.After(prog.Deadline.Time) {
		return DrainDecision{Action: ActionHold, Phase: PhaseBlocked, Reason: "drain-timeout-exceeded"}
	}

	// TODO(step-3)：在途核对与目标选择（门 6–7），实现前相关测试是红的——刻意如此，
	// 不为全绿预填本人核心。门次序与语义见上；三条线索：
	//   门 6（prog.ActivePod != nil 时）按目标节点事实分支：
	//     a) 旧 UID 仍在且 Terminating → none/Draining old-pod-terminating，等待；
	//     b) 旧 UID 仍在且非 Terminating → 先 ClassifyPod 重新分类：仍受支持才
	//        evict 重发同一 UID；变成 blocker 就 Blocked 交人工（UID 没变不等于
	//        归属没变）；变成保留类则 ClearActive 交下一轮重评估；
	//     c) 旧 UID 已消失 → 不重发旧目标：同名换了对象就 ClearActive 交下一轮重评估，
	//        没有同名者直接落入门 7；
	//   门 7（无在途）先全量扫 sorted 再动手，次序不能乱：
	//     ① 任何 ClassifyPod 的 blocker → 先 Blocked（不能见 supported 就选，
	//        排序靠后/别的 namespace 的未知对象同样要挡住）；
	//     ② 目标节点已有 Terminating 的业务对象（非 DaemonSet/镜像）→
	//        none/Draining pod-terminating 等待（不问它是不是本申请发起的）；
	//     ③ 替代缺口：countOwnedReady(f,prog,true)+countOwnedReady(f,prog,false)
	//        < f.TargetDesired → none/WaitingForReplacement replacement-not-ready
	//        （驱逐下一个之前，上一轮的缺口要已被实际 Ready 副本补齐）；
	//     ④ 才轮到 ClassifyPod 给 supported 的第一个（名字序）成为本轮唯一目标；
	//     ⑤ 无 supported → 用 evacuationComplete 的缺口映射等待状态：
	//        replacement-not-ready-elsewhere→WaitingForReplacement/replacement-not-ready，
	//        deployment-observation-lagging→WaitingForReplacement/observation-lagging，
	//        target-scaled-to-zero→Blocked/target-scaled-to-zero。
	if prog.ActivePod != nil {
		for _, pod := range f.PodsOnTargetNode {
			uidAlive := pod.UID == prog.ActivePod.UID

			if uidAlive && pod.Terminating {
				return DrainDecision{
					Action:  ActionNone,
					Phase:   PhaseDraining,
					Reason:  "old-pod-terminating",
					Requeue: true,
				}
			}

			if uidAlive && !pod.Terminating {
				supported, blocker := ClassifyPod(pod, prog.DeploymentUID)
				if supported {
					return DrainDecision{
						Action: ActionEvict,
						Phase:  PhaseDraining,
						Reason: "eviction-retry",
						Evict: &ActivePodRef{
							Namespace: pod.Namespace,
							Name:      pod.Name,
							UID:       pod.UID,
						},
					}
				}
				if !supported && blocker != "" {
					return DrainDecision{
						Action: ActionHold,
						Phase:  PhaseBlocked,
						Reason: blocker,
					}
				}
				if !supported && blocker == "" {
					return DrainDecision{
						Action:      ActionHold,
						Phase:       PhaseDraining,
						Reason:      "active-pod-out-of-scope",
						ClearActive: true,
						Requeue:     true,
					}
				}
			}
			if !uidAlive && pod.Name == prog.ActivePod.Name && pod.Namespace == prog.ActivePod.Namespace {
				return DrainDecision{
					Action:      ActionHold,
					Phase:       PhaseDraining,
					Reason:      "active-pod-uid-changed",
					ClearActive: true,
					Requeue:     true,
				}
			}
		}
	}

	// 门7

	sorted := append([]PodFact(nil), f.PodsOnTargetNode...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	for _, pod := range sorted {
		supported, blocker := ClassifyPod(pod, prog.DeploymentUID)
		if !supported && blocker != "" {
			return DrainDecision{
				Action: ActionHold,
				Phase:  PhaseBlocked,
				Reason: blocker,
			}
		}
	}
	for _, pod := range sorted {
		if !pod.Mirror && !pod.IsDaemonSet() && pod.Terminating {
			return DrainDecision{
				Action:  ActionNone,
				Phase:   PhaseDraining,
				Reason:  "pod-terminating",
				Requeue: true,
			}
		}
	}
	for _, pod := range sorted {
		supported, _ := ClassifyPod(pod, prog.DeploymentUID)
		if supported {
			if countOwnedReady(f, prog, true)+countOwnedReady(f, prog, false) < f.TargetDesired {
				return DrainDecision{
					Action:  ActionNone,
					Phase:   PhaseWaitingForReplacement,
					Reason:  "replacement-not-ready",
					Requeue: true,
				}
			} else {
				return DrainDecision{
					Action: ActionEvict,
					Phase:  PhaseDraining,
					Reason: "supported-pod-on-node",
					Evict: &ActivePodRef{
						Namespace: pod.Namespace,
						Name:      pod.Name,
						UID:       pod.UID,
					},
				}
			}
		}
	}

	// 无候选，按缺口映射等待状态

	_, missing := evacuationComplete(f, prog)

	switch missing {
	case "replacement-not-ready-elsewhere":
		return DrainDecision{
			Action:  ActionNone,
			Phase:   PhaseWaitingForReplacement,
			Reason:  "replacement-not-ready",
			Requeue: true,
		}
	case "deployment-observation-lagging":
		return DrainDecision{
			Action:  ActionNone,
			Phase:   PhaseWaitingForReplacement,
			Reason:  "observation-lagging",
			Requeue: true,
		}
	case "target-scaled-to-zero":
		return DrainDecision{
			Action: ActionHold,
			Phase:  PhaseBlocked,
			Reason: "target-scaled-to-zero",
		}
	default:
		return DrainDecision{
			Action:  ActionHold,
			Phase:   PhaseDraining,
			Reason:  "drain-in-progress",
			Requeue: true,
		}
	}
}

// evacuationComplete：受限完成判断——WorkloadEvacuated 的事实前提，缺一条就不成立：
//  1. 目标节点上无受支持业务 Pod、无保留规则之外的阻塞对象（观察面跨 namespace）；
//  2. 本轮在途旧 Pod 身份已消失；
//  3. 目标 Deployment UID 与进度绑定一致、status.observedGeneration 追上 spec
//     （观察没追上就宣告完成，等于拿旧报告当新事实）、desired>0；
//  4. 所需副本已在非目标节点 Ready——数实际替代 Pod（facts.go 的 countOwnedReady
//     (f,prog,false)），不拿 Deployment 的 Ready 计数减目标节点数冒充：同一代次内
//     Pod 健康变化不 bump generation，observedGeneration 追上也不代表 status
//     已反映此刻每个 Pod 的事实。
//
// 反例约束：副本数 0 不算完成（desired<=0 单独报告）；读取失败在观察面上抛、
// 到不了这里；PDB 预算不参与完成判断。Service 请求样本是另一层业务证据，
// 不在本函数内——不能仅凭总 Ready 计数保证全程可用。
func evacuationComplete(f *Facts, prog *EvictionProgress) (bool, string) {
	// TODO(step-3)：实现受限完成判断（判据清单见上）。实现前相关测试是红的
	for _, pod := range f.PodsOnTargetNode {
		supported, blocker := ClassifyPod(pod, prog.DeploymentUID)
		if !supported && blocker != "" {
			return false, blocker
		}
		if supported {
			return false, "supported-pod-still-on-node"
		}
	}
	if prog.ActivePod != nil {
		for _, pod := range f.PodsOnTargetNode {
			if pod.UID == prog.ActivePod.UID {
				return false, "old-pod-identity-still-present"
			}
		}
	}
	if f.TargetUID != prog.DeploymentUID {
		return false, "deployment-uid-changed"
	}
	if f.TargetObservedGeneration < f.TargetGeneration {
		return false, "deployment-observation-lagging"
	}
	if f.TargetDesired <= 0 {
		return false, "target-scaled-to-zero"
	}
	if countOwnedReady(f, prog, false) < f.TargetDesired {
		return false, "replacement-not-ready-elsewhere"
	}

	return true, ""
}

// submissionStillAuthorized：W43 提交前复核——原样保留。
func submissionStillAuthorized(f Facts, node *corev1.Node) bool {
	return string(node.UID) == f.NodeUID &&
		!node.Spec.Unschedulable &&
		node.Annotations[MarkerAnnotation] == f.MarkerPlanUID
}

// statusEqual：无变化不写的判据（含意图与疏散进度）。
func statusEqual(a, b MaintenancePlanStatus) bool {
	aCi, bCi := "", ""
	if a.CordonIntent != nil {
		aCi = a.CordonIntent.NodeUID
	}
	if b.CordonIntent != nil {
		bCi = b.CordonIntent.NodeUID
	}
	return a.Phase == b.Phase && a.Reason == b.Reason &&
		a.ObservedGeneration == b.ObservedGeneration && aCi == bCi &&
		progressEqual(a.EvictionProgress, b.EvictionProgress)
}

func progressEqual(a, b *EvictionProgress) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	var ap, bp ActivePodRef
	hasA, hasB := a.ActivePod != nil, b.ActivePod != nil
	if hasA {
		ap = *a.ActivePod
	}
	if hasB {
		bp = *b.ActivePod
	}

	return a.PlanGeneration == b.PlanGeneration &&
		a.DeploymentUID == b.DeploymentUID &&
		a.StartedAt.Equal(&b.StartedAt) && a.Deadline.Equal(&b.Deadline) &&
		hasA == hasB && ap == bp &&
		a.NextActionAt.Equal(b.NextActionAt)
}

// copyStatus：写前快照——进度是指针，先深拷再改，才能做"无变化不写"的比较。
func copyStatus(s MaintenancePlanStatus) MaintenancePlanStatus {
	out := s
	if s.CordonIntent != nil {
		ci := *s.CordonIntent
		out.CordonIntent = &ci
	}
	out.EvictionProgress = s.EvictionProgress.DeepCopy()
	return out
}

// requeueAfter：有界再排队默认间隔（W43 教学取舍；429 带 Retry-After 提示时按提示）。
const requeueAfter = 30 * time.Second

// evictCallTimeout：单次 API 调用限时——与整轮疏散截止是两个不同的时间。
const evictCallTimeout = 10 * time.Second

// maxHintSeconds：429 提示延迟的上限——提示过大时按上限取（有界重排队）。
const maxHintSeconds = 120
