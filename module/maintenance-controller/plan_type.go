package main

// plan_types.go：MaintenancePlan 的 Go 类型与 Scheme 注册。
// W43（0021）：nodeName/targetRef、Pending/Blocked/Cordoned、cordonIntent。
// W44 新增：spec.allowEviction（缺省 false——旧申请止于 Cordoned，兼容升级）与
// spec.drainTimeoutSeconds（整轮疏散的固定截止；教学示例取值 600 秒，非通用推荐）；
// status.evictionProgress 记录一轮疏散绑定的身份与截止，在调用前保存，
// 用于恢复判断——不代替重新读取 API 事实，也不是执行成功的凭据。

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName    = "maintenance.studymate.io"
	GroupVersion = "v1alpha1"
	// MarkerAnnotation：Node 上的所有权标记——值是认领这份 cordon 的 Plan UID。
	MarkerAnnotation = "maintenance.studymate.io/cordon-plan-uid"
	// DefaultDrainTimeoutSeconds：drainTimeoutSeconds 未填时的教学取值（600 秒），
	// 只是本项目的示例默认，不是 Kubernetes 官方推荐值。
	DefaultDrainTimeoutSeconds int64 = 600
)

var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: GroupVersion}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(SchemeGroupVersion, &MaintenancePlan{}, &MaintenancePlanList{})
	metav1.AddToGroupVersion(s, SchemeGroupVersion)
	return nil
}

// TargetRef：观察哪组负载（预检范围）。
type TargetRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// MaintenancePlanSpec：申请者写的区——目标与许可。
type MaintenancePlanSpec struct {
	NodeName  string    `json:"nodeName"`
	TargetRef TargetRef `json:"targetRef"`
	// AllowEviction：显式驱逐许可。缺省 false——旧申请不带此字段就止于 Cordoned，
	// 升级程序不能让历史 cordon 申请自动开始驱逐。
	AllowEviction bool `json:"allowEviction,omitempty"`
	// DrainTimeoutSeconds：整轮疏散截止（正整数秒）。从 drain 开始时刻起算，
	// 固定不变；进程重启不重置。
	DrainTimeoutSeconds *int64 `json:"drainTimeoutSeconds,omitempty"`
}

// Phase：进展。W43 终点 Cordoned；W44 教学状态续接：
// Draining（驱逐进行/在途）→ WaitingForReplacement（旧身份已消失，等替代 Ready）
// → WorkloadEvacuated（仅指受支持业务范围完成疏散，不表示节点可关机或维护完成）。
// 后三个是本项目的教学状态，不是原生 Kubernetes 状态。
type Phase string

const (
	PhasePending  Phase = "Pending"
	PhaseBlocked  Phase = "Blocked"
	PhaseCordoned Phase = "Cordoned"

	PhaseDraining              Phase = "Draining"
	PhaseWaitingForReplacement Phase = "WaitingForReplacement"
	PhaseWorkloadEvacuated     Phase = "WorkloadEvacuated"
)

// CordonIntent：受限意图——执行 cordon 前记下的"我观察到的目标身份"。
type CordonIntent struct {
	NodeUID string `json:"nodeUID,omitempty"`
}

// ActivePodRef：在途驱逐目标的身份。名字定位，UID 才是身份。
type ActivePodRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// EvictionProgress：一轮疏散的最小持久进度。
// PlanGeneration/DeploymentUID 是本轮绑定的身份——处理期间变了就交人工；
// StartedAt/Deadline 是固定时间窗——重启后不重置；ActivePod 至多一份。
type EvictionProgress struct {
	PlanGeneration int64         `json:"planGeneration"`
	DeploymentUID  string        `json:"deploymentUID"`
	StartedAt      metav1.Time   `json:"startedAt"`
	Deadline       metav1.Time   `json:"deadline"`
	ActivePod      *ActivePodRef `json:"activePod,omitempty"`
	NextActionAt   *metav1.Time  `json:"nextActionAt,omitempty"`
}

// MaintenancePlanStatus：协调器写的区——进展报告。
type MaintenancePlanStatus struct {
	Phase              Phase             `json:"phase,omitempty"`
	Reason             string            `json:"reason,omitempty"`
	ObservedGeneration int64             `json:"observedGeneration,omitempty"`
	CordonIntent       *CordonIntent     `json:"cordonIntent,omitempty"`
	EvictionProgress   *EvictionProgress `json:"evictionProgress,omitempty"`
}

// MaintenancePlan：申请对象。
type MaintenancePlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MaintenancePlanSpec   `json:"spec"`
	Status            MaintenancePlanStatus `json:"status,omitempty"`
}

// MaintenancePlanList：List 形态。
type MaintenancePlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MaintenancePlan `json:"items"`
}

// DeepCopy 手写版（教学取舍：不引入 controller-gen）。

func (in *EvictionProgress) DeepCopyInto(out *EvictionProgress) {
	*out = *in
	out.StartedAt = in.StartedAt
	out.Deadline = in.Deadline
	out.NextActionAt = in.NextActionAt
	if in.ActivePod != nil {
		ap := *in.ActivePod
		out.ActivePod = &ap
	}
	if in.NextActionAt != nil {
		na := *in.NextActionAt
		out.NextActionAt = &na
	}
}

func (in *EvictionProgress) DeepCopy() *EvictionProgress {
	if in == nil {
		return nil
	}
	out := new(EvictionProgress)
	in.DeepCopyInto(out)
	return out
}

func (in *MaintenancePlan) DeepCopyInto(out *MaintenancePlan) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	if in.Status.CordonIntent != nil {
		ci := *in.Status.CordonIntent
		out.Status.CordonIntent = &ci
	}
	if in.Status.EvictionProgress != nil {
		out.Status.EvictionProgress = in.Status.EvictionProgress.DeepCopy()
	}
}

func (in *MaintenancePlan) DeepCopy() *MaintenancePlan {
	if in == nil {
		return nil
	}
	out := new(MaintenancePlan)
	in.DeepCopyInto(out)
	return out
}

func (in *MaintenancePlan) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *MaintenancePlanList) DeepCopyInto(out *MaintenancePlanList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]MaintenancePlan, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *MaintenancePlanList) DeepCopy() *MaintenancePlanList {
	if in == nil {
		return nil
	}
	out := new(MaintenancePlanList)
	in.DeepCopyInto(out)
	return out
}

func (in *MaintenancePlanList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
