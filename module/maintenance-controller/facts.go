package main

// facts.go：观察面——0021 preflight 的 W44 扩展。分层约定不变：只取事实、失败上抛，不做判断。
// 新增事实：目标 Deployment 的 UID/代次（身份与观察是否追上 spec），
// 以及实际调度在目标节点上的 Pod 与目标 Deployment 名下的 Pod 及其归属链——
// Pod→ReplicaSet→Deployment 沿 OwnerReferences 的 UID 逐层核对，不用标签或
// 模板匹配冒充所有权（selector 只用来圈候选，身份以归属链为准）。
//
// 观察范围（修订 R1/R3）：目标节点上的 Pod 按 spec.nodeName 跨 namespace 列出——
// 节点阻塞契约看的是"这台节点上有什么"，不看它在哪个 namespace；
// 替代观察按目标 Deployment 的 selector 在其 namespace 内圈候选再逐个核对归属链。
// 两处范围都与 main 的集群级缓存（List/Watch 不限 namespace）和 RBAC 清单对齐。

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodFact：一个 Pod 的身份与归属事实。
type PodFact struct {
	Namespace   string
	Name        string
	UID         string
	NodeName    string // 实际落点——替代观察要区分"在/不在目标节点"
	Ready       bool   // PodReady 条件为 True
	Terminating bool   // deletionTimestamp 已设——驱逐受理后的正常中间态

	ControllerKind string // 第一层 controller owner 的 Kind（ReplicaSet/DaemonSet/StatefulSet；空=裸 Pod）
	ControllerName string
	ControllerUID  string

	// OwnerDeploymentUID：沿 RS→Deployment 逐层 UID 核对到的归属；空=没核对到
	// （含 DaemonSet/StatefulSet/裸 Pod/核对失败——都算"未归属到目标 Deployment"）。
	OwnerDeploymentUID string

	Mirror       bool // 静态 Pod 的镜像标记（kubernetes.io/config.mirror 注解）
	LocalVolumes bool // 挂了 hostPath / PVC / emptyDir——本项目不支持的负载
}

// IsDaemonSet：属于明确保留规则（驱逐后会在本节点原样重建，不属疏散范围）。
func (p PodFact) IsDaemonSet() bool { return p.ControllerKind == "DaemonSet" }

// Facts：一轮采集到的全部事实。
type Facts struct {
	NodeUID           string
	NodeReady         bool
	NodeUnschedulable bool
	MarkerPlanUID     string

	TargetReady   int32
	TargetDesired int32
	TargetUID     string // 目标 Deployment UID——W44 进度绑定的身份
	// TargetGeneration/TargetObservedGeneration：Deployment 的 spec 代次与
	// 控制器已观察到的代次——observed 落后说明 status 还没追上当前 spec。
	TargetGeneration         int64
	TargetObservedGeneration int64

	PdbMatched bool
	PdbBudget  int32

	// TargetNodeName：本申请的目标节点名——替代观察区分落点用。
	TargetNodeName string

	// PodsOnTargetNode：field selector spec.nodeName 圈出的、实际在该节点上的 Pod
	//（跨 namespace——节点上有什么，与它在哪个 namespace 无关）。
	PodsOnTargetNode []PodFact

	// PodsOfTargetDeployment：目标 Deployment 的 selector 在其 namespace 内圈中的
	// 候选 Pod，逐个沿归属链核对后的事实（含仍落在目标节点上的）。
	// selector 只是圈候选的读取范围；算不算"目标名下"，以 OwnerDeploymentUID 为准。
	// 多对象观察不是事务——两次 List 之间世界可以变化，判断面不假设它一致。
	PodsOfTargetDeployment []PodFact
}

// ReadFacts：按 spec 的观察范围采集。任何一步读不到都是错误——不许用零值冒充观察。
// 归属链中的 NotFound 是合法事实（对象已删，链断在此处，归属留空）；
// 其他读取错误原样上抛。
func ReadFacts(ctx context.Context, c client.Client, plan *MaintenancePlan) (Facts, error) {
	var f Facts

	var node corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: plan.Spec.NodeName}, &node); err != nil {
		return Facts{}, fmt.Errorf("读取节点 %s: %w", plan.Spec.NodeName, err)
	}
	f.NodeUID = string(node.UID)
	f.NodeUnschedulable = node.Spec.Unschedulable
	if node.Annotations != nil {
		f.MarkerPlanUID = node.Annotations[MarkerAnnotation]
	}
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			f.NodeReady = node.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}

	var dep appsv1.Deployment
	depKey := client.ObjectKey{Namespace: plan.Spec.TargetRef.Namespace, Name: plan.Spec.TargetRef.Name}
	if err := c.Get(ctx, depKey, &dep); err != nil {
		return Facts{}, fmt.Errorf("读取目标 Deployment %s/%s: %w", depKey.Namespace, depKey.Name, err)
	}
	f.TargetUID = string(dep.UID)
	f.TargetGeneration = dep.Generation
	f.TargetObservedGeneration = dep.Status.ObservedGeneration
	f.TargetReady = dep.Status.ReadyReplicas
	if dep.Spec.Replicas != nil {
		f.TargetDesired = *dep.Spec.Replicas
	}
	f.TargetNodeName = plan.Spec.NodeName

	// PDB 匹配：selector 圈中目标模板标签才算约束（W42 的 R6 同一口径）。
	var pdbs policyv1.PodDisruptionBudgetList
	if err := c.List(ctx, &pdbs, client.InNamespace(plan.Spec.TargetRef.Namespace)); err != nil {
		return Facts{}, fmt.Errorf("列出 PDB: %w", err)
	}
	tpl := labels.Set(dep.Spec.Template.Labels)
	for i := range pdbs.Items {
		pdb := &pdbs.Items[i]
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			return Facts{}, fmt.Errorf("解析 PDB %s 的 selector: %w", pdb.Name, err)
		}
		if sel.Matches(tpl) {
			budget := pdb.Status.DisruptionsAllowed
			if !f.PdbMatched || budget < f.PdbBudget {
				f.PdbMatched = true
				f.PdbBudget = budget
			}
		}
	}

	// 目标节点上的 Pod（field selector 需要 main/测试各自注册 spec.nodeName 索引）。
	// 刻意不带 namespace 过滤：节点阻塞契约的观察对象是"这台节点上的一切"——
	// 其他 namespace 的裸 Pod 落在这台节点上，同样是本申请范围外的负载（R1③）。
	var pods corev1.PodList
	if err := c.List(ctx, &pods,
		client.MatchingFields{"spec.nodeName": plan.Spec.NodeName}); err != nil {
		return Facts{}, fmt.Errorf("列出节点 %s 上的 Pod: %w", plan.Spec.NodeName, err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		pf := podFactOf(pod)
		if err := resolveOwnership(ctx, c, pod, &pf); err != nil {
			return Facts{}, err
		}
		f.PodsOnTargetNode = append(f.PodsOnTargetNode, pf)
	}

	// 目标 Deployment 名下的 Pod（替代观察用）：selector 圈候选（限其 namespace），
	// 归属链逐个核对——完成判据要的是"实际替代 Pod 的身份与落点与 Ready"，
	// 不能拿 Deployment 的 Ready 计数减一下来冒充（R3：同一代次内 Pod 健康变化
	// 不会 bump generation，observedGeneration 追上也不保证 status 各字段反映此刻）。
	depSel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return Facts{}, fmt.Errorf("解析 Deployment %s/%s 的 selector: %w", depKey.Namespace, depKey.Name, err)
	}
	var owned corev1.PodList
	if err := c.List(ctx, &owned,
		client.InNamespace(plan.Spec.TargetRef.Namespace),
		&client.MatchingLabelsSelector{Selector: depSel}); err != nil {
		return Facts{}, fmt.Errorf("列出目标 Deployment 名下的 Pod: %w", err)
	}
	for i := range owned.Items {
		pod := &owned.Items[i]
		pf := podFactOf(pod)
		if err := resolveOwnership(ctx, c, pod, &pf); err != nil {
			return Facts{}, err
		}
		f.PodsOfTargetDeployment = append(f.PodsOfTargetDeployment, pf)
	}
	return f, nil
}

// podFactOf：Pod 自身可读出的身份事实。
func podFactOf(pod *corev1.Pod) PodFact {
	pf := PodFact{
		Namespace:    pod.Namespace,
		Name:         pod.Name,
		UID:          string(pod.UID),
		NodeName:     pod.Spec.NodeName,
		Terminating:  pod.DeletionTimestamp != nil,
		Mirror:       pod.Annotations["kubernetes.io/config.mirror"] != "",
		LocalVolumes: hasLocalVolume(pod),
	}
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			pf.Ready = pod.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	if ref := controllerRef(pod.OwnerReferences); ref != nil {
		pf.ControllerKind = ref.Kind
		pf.ControllerName = ref.Name
		pf.ControllerUID = string(ref.UID)
	}
	return pf
}

// resolveOwnership：沿 Pod→ReplicaSet→Deployment 逐层核对 UID。
// 任一层对象不存在（NotFound）：链断，归属留空——这是事实，不是错误。
// 其他错误：上抛。
func resolveOwnership(ctx context.Context, c client.Client, pod *corev1.Pod, pf *PodFact) error {
	if pf.ControllerKind != "ReplicaSet" || pf.ControllerName == "" {
		return nil // DaemonSet/StatefulSet/裸 Pod：不沿 Deployment 链归属
	}
	var rs appsv1.ReplicaSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pf.ControllerName}, &rs); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("读取 ReplicaSet %s/%s: %w", pod.Namespace, pf.ControllerName, err)
	}
	if string(rs.UID) != pf.ControllerUID {
		return nil // 同名不同对象：OwnerRef 的 UID 核对不上，不算所有权
	}
	dref := controllerRef(rs.OwnerReferences)
	if dref == nil || dref.Kind != "Deployment" {
		return nil
	}
	var dep appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: rs.Namespace, Name: dref.Name}, &dep); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("读取 Deployment %s/%s: %w", rs.Namespace, dref.Name, err)
	}
	if string(dep.UID) == string(dref.UID) {
		pf.OwnerDeploymentUID = string(dep.UID)
	}
	return nil
}

// controllerRef：第一个 controller owner 引用；没有则 nil。
func controllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

// hasLocalVolume：hostPath / PVC / emptyDir——本项目只支持无本地数据风险的业务 Pod。
// hostPath 绑节点数据；PVC 的重调度语义超出本周范围；emptyDir 的内容随 Pod 销毁——
// 三类都算"带本地数据风险"，驱逐它等于替负载做一次数据决定（R1④）。
func hasLocalVolume(pod *corev1.Pod) bool {
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.HostPath != nil || v.PersistentVolumeClaim != nil || v.EmptyDir != nil {
			return true
		}
	}
	return false
}

// ClassifyPod：一个 Pod 对本申请的含义（纯函数，判断面用）。
// 返回 (supported, blockerReason)：
//   - supported=true 仅当归属链核对到目标 Deployment 且无本地数据风险卷——可以驱逐；
//   - DaemonSet / 镜像 Pod：明确保留规则——既不驱逐、也不算阻塞（驱逐后原样重建，留在原地）；
//   - 其余一律阻塞交人工：裸 Pod、StatefulSet、其他业务、归属不明、带本地数据卷的目标 Pod。
func ClassifyPod(p PodFact, targetDeploymentUID string) (supported bool, blocker string) {
	switch {
	case p.Mirror:
		return false, ""
	case p.IsDaemonSet():
		return false, ""
	case p.ControllerKind == "StatefulSet":
		return false, "statefulset-pod-present"
	case p.OwnerDeploymentUID == targetDeploymentUID:
		if p.LocalVolumes {
			return false, "target-pod-local-volumes"
		}
		return true, ""
	case p.ControllerKind == "":
		return false, "bare-pod-present"
	case p.OwnerDeploymentUID == "":
		return false, "unknown-ownership"
	default:
		return false, "foreign-workload-present"
	}
}

// countOwnedReady：归属链核对到目标 Deployment、Ready 且非 Terminating 的实际
// 副本数（R3 的观察基础，给定——与 ClassifyPod 同层的纯事实计数）。
// onTarget=true 数目标节点上的，false 数非目标节点的
// （= 替代在别处 Ready 的实数）。Deployment 的 Ready 计数只作辅助对照，
// 不用"总数减一下"冒充替代事实——同一代次内 Pod 健康变化不 bump generation。
func countOwnedReady(f *Facts, prog *EvictionProgress, onTarget bool) int32 {
	var n int32
	for i := range f.PodsOfTargetDeployment {
		p := &f.PodsOfTargetDeployment[i]
		if p.OwnerDeploymentUID != prog.DeploymentUID || !p.Ready || p.Terminating {
			continue
		}
		if (p.NodeName == f.TargetNodeName) != onTarget {
			continue
		}
		n++
	}
	return n
}
