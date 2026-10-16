package main

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// ReaderParams 汇总读取目标：观察范围由 0014 定下（目标节点 + 目标 Deployment + 节点全命名空间 Pod + 匹配 PDB）。
type ReaderParams struct {
	Namespace  string
	Node       string
	Deployment string
}

// KubeReader 用 client-go 只读地填一份 Snapshot。
// 分层约定：本文件只负责"取得事实"，不做任何判断——判断是 0017 的 Analyze 的事。
type KubeReader struct {
	cs     *kubernetes.Clientset
	params ReaderParams
}

func NewKubeReader(cs *kubernetes.Clientset, p ReaderParams) *KubeReader {
	return &KubeReader{cs: cs, params: p}
}

func controllerOwner(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		ref := &refs[i]
		if ref.Controller != nil && *ref.Controller {
			return ref
		}
	}
	return nil
}

// ReadSnapshot 按 step-1～4 采集。每一步失败：追加 ReadError 后尽力继续其余步骤；
// Analyze 看到 Errors 非空会给 Unknown，不会把零值当数据用。
//
// 各 step 的完整相邻代码看 demo-readonly-snapshot/main.go（0016 课件第 5 节逐段讲解），
// 实现时可以直接对照改写，但请自己敲一遍——装配感是这一课的练习目标。
func (r *KubeReader) ReadSnapshot(ctx context.Context) Snapshot {
	s := Snapshot{TakenAt: time.Now().UTC()}

	// TODO(step-1)：读目标 Node：CoreV1().Nodes().Get(ctx, r.params.Node, metav1.GetOptions{})。
	//   填 s.Node.Name / Ready / Unschedulable。
	//   Ready 要在 status.conditions 里找 type=NodeReady 的那一项，看它的 status 是否为 True。
	//   失败：s.Errors = append(s.Errors, ReadError{Step: "read-node", Message: err.Error()})。
	node, err := r.cs.CoreV1().Nodes().Get(ctx, r.params.Node, metav1.GetOptions{})
	if err != nil {
		fmt.Println("读取目标 Node 失败:", err)
		s.Errors = append(s.Errors, ReadError{Step: "read-node", Message: err.Error()})
	} else {
		s.Node.Name = node.Name
		s.Node.Unschedulable = node.Spec.Unschedulable
		for i := range node.Status.Conditions {
			if node.Status.Conditions[i].Type == corev1.NodeReady {
				s.Node.Ready = (node.Status.Conditions[i].Status == corev1.ConditionTrue)
			}
		}
	}

	// TODO(step-2)：读目标 Deployment：AppsV1().Deployments(r.params.Namespace).Get(...)。
	//   填 s.Deployment 字段（包括 metadata.uid → UID）（注意 Spec.Replicas 是 *int32 指针）。
	dep, err := r.cs.AppsV1().Deployments(r.params.Namespace).Get(ctx, r.params.Deployment, metav1.GetOptions{})
	if err != nil {
		fmt.Println("读取目标 Deployment 失败:", err)
		s.Errors = append(s.Errors, ReadError{Step: "read-deployment", Message: err.Error()})
	} else {
		s.Deployment = DeploymentState{
			Name:      dep.Name,
			Namespace: dep.Namespace,
			UID:       string(dep.UID),
			DesiredReplicas: func() int32 {
				if dep.Spec.Replicas != nil {
					return *dep.Spec.Replicas
				}
				s.Errors = append(s.Errors, ReadError{Step: "read-deployment", Message: "spec.replicas 为 nil"})
				return 0
			}(),
			ReadyReplicas: dep.Status.ReadyReplicas,
			NodeSelector:  dep.Spec.Template.Spec.NodeSelector,
		}
	}

	// TODO(step-3)：列目标节点上全部命名空间的 Pod：
	//   CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + r.params.Node})。
	//   每个 Pod 填一条 PodState：Ready 条件、emptyDir/hostPath、PVC、标签、owner 引用。
	//   分类：ReplicaSet 管的核对 namespace、Pod→RS controller引用/UID、RS→Deployment controller引用/UID；
	//   目标Deployment的namespace/名字/UID都一致才是target；其他namespace直接other。
	//   查询或RS身份校验失败必须追加read-pod-owner错误，保留Pod行，不能只归other吞错；
	//   DaemonSet → daemonset；带 kubernetes.io/config.mirror 注解 → mirror；无控制器 → bare。

	nodePods, err := r.cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + r.params.Node})
	if err != nil {
		fmt.Println("列目标节点上全部命名空间的 Pod 失败:", err)
		s.Errors = append(s.Errors, ReadError{Step: "list-node-pods", Message: err.Error()})
	} else {
		getPodClass := func(pod *corev1.Pod) PodClass {
			owner := controllerOwner(pod.OwnerReferences)
			// ismirror
			if _, ismirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; ismirror {
				return PodClassMirror
			}
			// isdaemonset
			if owner != nil && owner.Kind == "DaemonSet" {
				return PodClassDaemonSet
			}
			// isbare
			if owner == nil {
				return PodClassBare
			}
			//  pod -> rs -> dep 链
			if owner.Kind == "ReplicaSet" {
				// namespace  与 Rs 做比较
				if pod.Namespace != s.Deployment.Namespace {
					return PodClassOther
				}
				rs, err := r.cs.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
				if err != nil {
					fmt.Println("读取 ReplicaSet 失败:", err)
					s.Errors = append(s.Errors, ReadError{Step: "read-pod-owner", Message: err.Error()})
					return PodClassOther
				}

				// rs Owner
				if owner.UID == "" || owner.UID != rs.UID {
					s.Errors = append(s.Errors, ReadError{Step: "read-pod-owner", Message: fmt.Sprintf("Pod %s/%s 的 ReplicaSet UID 不匹配", pod.Namespace, pod.Name)})
					return PodClassOther
				}

				// dep owner
				depOwner := controllerOwner(rs.OwnerReferences)
				if depOwner != nil &&
					depOwner.Kind == "Deployment" &&
					depOwner.UID == types.UID(s.Deployment.UID) &&
					depOwner.Name == s.Deployment.Name &&
					s.Deployment.UID != "" &&
					pod.Namespace == s.Deployment.Namespace {
					return PodClassTarget
				}
			}

			return PodClassOther
		}
		for _, pod := range nodePods.Items {
			// owner := controllerOwner(pod.OwnerReferences)
			s.Pods = append(s.Pods, PodState{
				Namespace: pod.Namespace,
				Name:      pod.Name,
				Ready: func() bool {
					for i := range pod.Status.Conditions {
						if pod.Status.Conditions[i].Type == corev1.PodReady {
							return (pod.Status.Conditions[i].Status == corev1.ConditionTrue)
						}
					}
					return false
				}(),
				HasLocalData: func() bool {
					for _, v := range pod.Spec.Volumes {
						if v.EmptyDir != nil || v.HostPath != nil {
							return true
						}
					}
					return false
				}(),
				Class: getPodClass(&pod),
				UsesPVC: func() bool {
					for _, vol := range pod.Spec.Volumes {
						if vol.PersistentVolumeClaim != nil {
							return true
						}
					}
					return false
				}(),
				Labels: pod.Labels,
				OwnerKind: func() string {
					owner := controllerOwner(pod.OwnerReferences)
					if owner != nil {
						return owner.Kind
					}
					return ""
				}(),
				OwnerName: func() string {
					owner := controllerOwner(pod.OwnerReferences)
					if owner != nil {
						return owner.Name
					}
					return ""
				}(),
			})
		}
	}

	// TODO(step-4)：为涉及的每个命名空间列 PDB：PolicyV1().PodDisruptionBudgets(ns).List(...)。
	//   只保留 selector 匹配到至少一个节点 Pod 的 PDB（matchLabels 存进 Selector；
	//   出现 matchExpressions 时置 SelectorExpr=true 交人工）。填 status 三个数。
	namespaces := map[string]struct{}{
		r.params.Namespace: {},
	}
	for _, pod := range s.Pods {
		namespaces[pod.Namespace] = struct{}{}
	}
	// 跨namespace查询
	for ns := range namespaces {
		pdbs, err := r.cs.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Println("列 PDB 失败:", err)
			s.Errors = append(s.Errors, ReadError{Step: "list-pdbs", Message: err.Error()})
		} else {
			for _, pdb := range pdbs.Items {
				if pdb.Spec.Selector == nil {
					continue
				}
				hasExpressions := len(pdb.Spec.Selector.MatchExpressions) > 0
				if !hasExpressions {
					matcher := labels.SelectorFromSet(pdb.Spec.Selector.MatchLabels)
					// 检查是否有匹配到节点上的 Pod
					matched := false
					for _, pod := range s.Pods {
						if pod.Namespace == pdb.Namespace && matcher.Matches(labels.Set(pod.Labels)) {
							matched = true
							break
						}
					}
					if !matched {
						continue
					}
				}

				s.PDBs = append(s.PDBs, PDBState{
					Namespace:          pdb.Namespace,
					Name:               pdb.Name,
					CurrentHealthy:     pdb.Status.CurrentHealthy,
					DesiredHealthy:     pdb.Status.DesiredHealthy,
					DisruptionsAllowed: pdb.Status.DisruptionsAllowed,
					Selector:           pdb.Spec.Selector.MatchLabels,
					SelectorExpr:       len(pdb.Spec.Selector.MatchExpressions) > 0,
				})

			}
		}
	}

	return s
}
