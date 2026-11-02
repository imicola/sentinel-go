package main

// reconciler.go：协调循环。一轮 = 重读申请 → 读事实 → 分阶段判断 →
// （要动手时）先保存身份/进度、现读复核、再执行 → 报告 → 决定要不要再来。
// W43 cordon 阶段与 0021 语义一致；W44 疏散阶段在进度存在后接管，借用 PodEvictor。
// 两套客户端的分工：controller-runtime Client 管持久对象读写，
// typed clientset（经 PodEvictor）管这次对 Pod 子资源的 POST。

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// MaintenancePlanReconciler：写入型协调器。
// Evictor 由 main 装配注入（真实集群）；测试注入 fakeEvictor。
type MaintenancePlanReconciler struct {
	client.Client
	Evictor PodEvictor
}

// Reconcile：一轮协调。阶段选择只看一个持久事实——疏散进度是否存在。
func (r *MaintenancePlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var plan MaintenancePlan
	if err := r.Get(ctx, req.NamespacedName, &plan); err != nil {
		if client.IgnoreNotFound(err) == nil {
			l.Info("申请已删除，本轮无事可做", "request", req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("重读申请: %w", err)
	}

	facts, err := ReadFacts(ctx, r.Client, plan.DeepCopy())
	if err != nil {
		return ctrl.Result{}, err // 读取失败是错误——不用零值冒充观察，更不报完成
	}

	if plan.Status.EvictionProgress == nil {
		return r.reconcileCordonStage(ctx, l, &plan, facts)
	}
	return r.reconcileDrainStage(ctx, l, &plan, facts)
}

// reconcileCordonStage：W43 cordon 阶段（0021 流程原样）＋ W44 进入点——
// cordon 归属本申请且申请显式 allowEviction 时，先保存疏散进度再进入下一阶段。
func (r *MaintenancePlanReconciler) reconcileCordonStage(ctx context.Context, l logr.Logger, plan *MaintenancePlan, f Facts) (ctrl.Result, error) {
	d := Decide(plan, f)

	if d.Action == ActionCordon {
		// 先保存受限意图，再重读复核，再提交——失败不报成功。
		if plan.Status.CordonIntent == nil || plan.Status.CordonIntent.NodeUID != f.NodeUID {
			plan.Status.CordonIntent = &CordonIntent{NodeUID: f.NodeUID}
			plan.Status.ObservedGeneration = plan.Generation
			if err := r.Status().Update(ctx, plan); err != nil {
				if errors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("更新 status.CordonIntent: %w", err)
			}
			l.Info("已保存受限意图", "nodeUID", f.NodeUID)
		}
		var node corev1.Node
		if err := r.Get(ctx, client.ObjectKey{Name: plan.Spec.NodeName}, &node); err != nil {
			return ctrl.Result{}, fmt.Errorf("重读 Node: %w", err)
		}
		if !submissionStillAuthorized(f, &node) {
			l.Info("重读后前置变化，本轮不提交", "node", plan.Spec.NodeName)
			return ctrl.Result{Requeue: true}, nil
		}
		if err := ApplyCordon(ctx, r.Client, plan, &node); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("ApplyCordon: %w", err)
		}
		d.Phase = PhaseCordoned
		d.Reason = "cordon-applied"
		d.Requeue = false
	}

	desired := MaintenancePlanStatus{
		Phase:              d.Phase,
		Reason:             d.Reason,
		ObservedGeneration: plan.Generation,
		CordonIntent:       plan.Status.CordonIntent,
	}
	if !statusEqual(desired, plan.Status) {
		plan.Status = desired
		if err := r.Status().Update(ctx, plan); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("更新 status: %w", err)
		}
	}

	// W44 进入点：正常终点 Cordoned（本申请的 cordon 已生效）且显式允许驱逐。
	// 不带 allowEviction 的旧申请走到 Cordoned 就停——升级程序不让旧申请开始驱逐。
	if d.Action == ActionNone && d.Phase == PhaseCordoned && plan.Spec.AllowEviction {
		return r.startDrain(ctx, l, plan, f)
	}
	if d.Requeue {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// startDrain：保存一轮疏散的绑定身份与固定截止——在任何驱逐调用之前。
// 冲突就重读重来；不先动作后补身份。
func (r *MaintenancePlanReconciler) startDrain(ctx context.Context, l logr.Logger, plan *MaintenancePlan, f Facts) (ctrl.Result, error) {
	timeout := DefaultDrainTimeoutSeconds
	if plan.Spec.DrainTimeoutSeconds != nil && *plan.Spec.DrainTimeoutSeconds > 0 {
		timeout = *plan.Spec.DrainTimeoutSeconds
	}
	now := time.Now()
	plan.Status.EvictionProgress = &EvictionProgress{
		PlanGeneration: plan.Generation,
		DeploymentUID:  f.TargetUID,
		StartedAt:      metav1.NewTime(now),
		Deadline:       metav1.NewTime(now.Add(time.Duration(timeout) * time.Second)),
	}
	plan.Status.Phase = PhaseDraining
	plan.Status.Reason = "drain-started"
	plan.Status.ObservedGeneration = plan.Generation
	if err := r.Status().Update(ctx, plan); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("保存疏散进度: %w", err)
	}
	l.Info("疏散开始：进度已先保存",
		"planGeneration", plan.Generation, "deadline", plan.Status.EvictionProgress.Deadline)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileDrainStage：疏散阶段。判断面给动作，这里只负责状态推进与重排队。
func (r *MaintenancePlanReconciler) reconcileDrainStage(ctx context.Context, l logr.Logger, plan *MaintenancePlan, f Facts) (ctrl.Result, error) {
	before := copyStatus(plan.Status)
	d := DecideDrain(plan, &f, time.Now())

	if d.ClearActive && plan.Status.EvictionProgress != nil {
		plan.Status.EvictionProgress.ActivePod = nil
	}
	if d.Action == ActionEvict {
		return r.evictOne(ctx, l, plan, d)
	}

	plan.Status.Phase = d.Phase
	plan.Status.Reason = d.Reason
	plan.Status.ObservedGeneration = plan.Generation
	if !statusEqual(before, plan.Status) {
		if err := r.Status().Update(ctx, plan); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("更新 status: %w", err)
		}
	}
	if d.Requeue {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// evictOne：本轮至多一次的驱逐调用。完整前置已由 DecideDrain 的门核过；
// 这里做提交前现读核对（UID 与落点；归属已在 Facts 沿链核对），
// 然后**先保存身份、再调用**，最后按返回分类推进状态。
func (r *MaintenancePlanReconciler) evictOne(ctx context.Context, l logr.Logger, plan *MaintenancePlan, d DrainDecision) (ctrl.Result, error) {
	// TODO(step-1)：执行本轮至多一次的 UID 绑定驱逐请求并处理返回。实现前相关测试是红的。
	// 顺序与语义（与 demo-evictcall 三幕对照）：
	//  ① 提交前现读核对：按 d.Evict 的名字 Get Pod——NotFound 本轮不请求（重排队）；
	//     UID 或落点（spec.nodeName）变了也不请求，重排队带新事实重新判断；
	//  ② 先保存身份再动作：把 d.Evict 写入 status.EvictionProgress.ActivePod
	//     并 Status().Update（409 → Requeue 重来）；
	//  ③ 用 context.WithTimeout(ctx, evictCallTimeout) 限时的这一次调用：
	//     r.Evictor.EvictPod(callCtx, ns, name, uid)；
	//  ④ 返回分类（缺一不可；R6 修订后原因认消息、提示只管延迟）：
	//     nil → Draining/eviction-request-accepted（只代表受理），默认间隔重排队；
	//     NotFound → 清 ActivePod，Draining/active-pod-vanished，重排队重新评估；
	//     429 → 清 ActivePod；消息含官方 PDB 拒绝文本（isPdbRefusal，
	//           "…violate the pod's disruption budget…"）→ pdb-budget-exhausted；
	//           认不出 → eviction-throttled（中性：来源不明就写来源不明——PDB 观察
	//           落后的 429 也带提示，提示不是来源证明）；SuggestsClientDelay 提示
	//           （有界，≤maxHintSeconds）只决定延迟秒数，不决定原因；
	//     403 → 清 ActivePod，Blocked/eviction-forbidden，不自动重试（交人工）；
	//     500 且 isKnownEvictionMisconfiguration（已知重叠 PDB 结构化消息）→
	//           清 ActivePod，Blocked/pdb-overlap-misconfigured，不自动重试
	//           （后续轮次由给定的门 4.5 挡住）；
	//     其他 → 错误上抛（结果未知：ActivePod 已持久，下一轮重读后才决定）。
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Name: d.Evict.Name, Namespace: d.Evict.Namespace}, pod); err != nil {
		if errors.IsNotFound(err) {
			l.Info("本轮不请求：ActivePod 已消失", "pod", client.ObjectKeyFromObject(pod))
			plan.Status.EvictionProgress.ActivePod = nil
			plan.Status.Phase = PhaseDraining
			plan.Status.Reason = "active-pod-vanished"
			plan.Status.ObservedGeneration = plan.Generation
			if err := r.Status().Update(ctx, plan); err != nil {
				if errors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("更新 status: %w", err)
			}
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		return ctrl.Result{}, fmt.Errorf("重读 ActivePod: %w", err)
	}
	if string(pod.UID) != d.Evict.UID || pod.Spec.NodeName != plan.Spec.NodeName {
		l.Info("本轮不请求：ActivePod 现读与计划不符", "pod", client.ObjectKeyFromObject(pod), "uid", pod.UID, "nodeName", pod.Spec.NodeName)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	before := copyStatus(plan.Status)
	plan.Status.EvictionProgress.ActivePod = d.Evict
	plan.Status.ObservedGeneration = plan.Generation
	plan.Status.Phase = PhaseDraining
	plan.Status.Reason = "eviction-request-submitted"
	if err := r.Status().Update(ctx, plan); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("更新 status.ActivePod: %w", err)
	}

	before = copyStatus(plan.Status)

	callCtx, cancel := context.WithTimeout(ctx, evictCallTimeout)
	defer cancel()
	if err := r.Evictor.EvictPod(
		callCtx,
		d.Evict.Namespace,
		d.Evict.Name,
		d.Evict.UID,
	); err != nil {
		if errors.IsNotFound(err) {
			l.Info("本轮不请求：ActivePod 已消失", "pod", client.ObjectKeyFromObject(pod))
			plan.Status.EvictionProgress.ActivePod = nil
			plan.Status.Phase = PhaseDraining
			plan.Status.Reason = "active-pod-vanished"
			plan.Status.ObservedGeneration = plan.Generation
			return r.writeDrainStatus(
				ctx,
				plan,
				before,
				ctrl.Result{RequeueAfter: requeueAfter},
			)
		}
		if errors.IsTooManyRequests(err) {
			l.Info("本轮不请求：ActivePod 驱逐被拒绝（429）", "pod", client.ObjectKeyFromObject(pod))
			plan.Status.EvictionProgress.ActivePod = nil
			plan.Status.Phase = PhaseDraining
			plan.Status.Reason = "eviction-throttled"
			plan.Status.ObservedGeneration = plan.Generation
			if isPdbRefusal(err) {
				plan.Status.Reason = "pdb-budget-exhausted"
			}
			delay := requeueAfter
			if seconds, ok := errors.SuggestsClientDelay(err); ok && seconds > 0 {
				if seconds > maxHintSeconds {
					seconds = maxHintSeconds
				}
				delay = time.Duration(seconds) * time.Second
			}
			return r.writeDrainStatus(
				ctx,
				plan,
				before,
				ctrl.Result{RequeueAfter: delay},
			)
		}
		if errors.IsForbidden(err) {
			l.Info("本轮不请求：ActivePod 驱逐被拒绝（403）", "pod", client.ObjectKeyFromObject(pod))
			plan.Status.EvictionProgress.ActivePod = nil
			plan.Status.Phase = PhaseBlocked
			plan.Status.Reason = "eviction-forbidden"
			plan.Status.ObservedGeneration = plan.Generation
			return r.writeDrainStatus(
				ctx,
				plan,
				before,
				ctrl.Result{},
			)
		}
		if isKnownEvictionMisconfiguration(err) {
			l.Info("本轮不请求：ActivePod 驱逐被拒绝（500，已知重叠 PDB 结构化消息）", "pod", client.ObjectKeyFromObject(pod))
			plan.Status.EvictionProgress.ActivePod = nil
			plan.Status.Phase = PhaseBlocked
			plan.Status.Reason = "pdb-overlap-misconfigured"
			plan.Status.ObservedGeneration = plan.Generation
			return r.writeDrainStatus(
				ctx,
				plan,
				before,
				ctrl.Result{},
			)
		}
		return ctrl.Result{}, fmt.Errorf("EvictPod: %w", err)
	}

	plan.Status.Phase = PhaseDraining
	plan.Status.Reason = "eviction-request-accepted"
	plan.Status.ObservedGeneration = plan.Generation
	return r.writeDrainStatus(
		ctx,
		plan,
		before,
		ctrl.Result{RequeueAfter: requeueAfter},
	)
}

// writeDrainStatus：统一的"无变化不写"落库。
func (r *MaintenancePlanReconciler) writeDrainStatus(ctx context.Context, plan *MaintenancePlan, before MaintenancePlanStatus, result ctrl.Result) (ctrl.Result, error) {
	if !statusEqual(before, plan.Status) {
		if err := r.Status().Update(ctx, plan); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("更新 status: %w", err)
		}
	}
	return result, nil
}
