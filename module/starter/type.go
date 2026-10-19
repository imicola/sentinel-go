package main

import "time"

// 本文件是 0015 定下的"输入与输出契约"的 Go 形态：只放数据类型，不放任何逻辑。
// Analyze 只依赖这些纯数据，所以测试可以直接构造 Snapshot 字面量，不需要 mock 框架。

// NodeState：目标节点的两个独立事实。
// Ready 回答"这台节点还能不能干活"（kubelet 上报）；
// Unschedulable 回答"集群要不要往这台派新活"（维护者设置的开关）。
type NodeState struct {
	Name          string
	Ready         bool
	Unschedulable bool
}

// DeploymentState：目标 Deployment 的声明与事实配对。
type DeploymentState struct {
	Namespace       string
	Name            string
	UID             string            // 从Deployment.metadata.uid取得
	DesiredReplicas int32             // spec.replicas（声明）
	ReadyReplicas   int32             // status.readyReplicas（事实）
	NodeSelector    map[string]string // 模板的节点约束（可为空）
}

// PodClass：节点上一个 Pod 的归属分类。分类决定规则怎么对待它。
type PodClass string

const (
	PodClassTarget    PodClass = "target"    // 属于目标 Deployment（Pod→RS→Deployment 链查验通过）
	PodClassDaemonSet PodClass = "daemonset" // DaemonSet 管理：drain 会跳过，报告但不阻塞
	PodClassMirror    PodClass = "mirror"    // static Pod 的 API 映射：drain 不碰，报告但不阻塞
	PodClassBare      PodClass = "bare"      // 无控制器管理：驱逐无人在意，交人工处理
	PodClassOther     PodClass = "other"     // 其他控制器/其他 Deployment：不在本工具支持清单
)

// PodState：节点上一个 Pod 的观察记录。
type PodState struct {
	Namespace    string
	Name         string
	Class        PodClass
	Ready        bool
	HasLocalData bool              // emptyDir / hostPath
	UsesPVC      bool              // persistentVolumeClaim
	Labels       map[string]string // 供 PDB selector 匹配
	OwnerKind    string            // 控制器类型（ReplicaSet/DaemonSet/…）
	OwnerName    string            // 控制器名
}

// PDBState：一份与节点上 Pod 匹配的中断预算。
// SelectorExpr 为 true 表示 selector 用了 matchExpressions——本教学版不解析，保守交人工。
type PDBState struct {
	Namespace          string
	Name               string
	CurrentHealthy     int32
	DesiredHealthy     int32
	DisruptionsAllowed int32
	Selector           map[string]string
	SelectorExpr       bool
}

// ReadError：一次 API 读取失败。Step 稳定可断言，Message 是错误原文。
// 契约：任何一步失败都保留在这里，不许用零值假装读到了数据。
type ReadError struct {
	Step    string
	Message string
}

// Snapshot：一次受限观察采集到的全部事实（只读）。
type Snapshot struct {
	TakenAt    time.Time
	Node       NodeState
	Deployment DeploymentState
	Pods       []PodState // 目标节点上全部命名空间的 Pod
	PDBs       []PDBState // 与这些 Pod 匹配的 PDB（按命名空间列出后按标签匹配）
	Errors     []ReadError
}

// Verdict：报告结论。四档语义见课件——ObservedReady 只表示"本次受限观察满足规则"，
// 不是自动维护许可。
type Verdict string

const (
	VerdictObservedReady Verdict = "ObservedReady"
	VerdictBlocked       Verdict = "Blocked"
	VerdictNeedsReview   Verdict = "NeedsReview"
	VerdictUnknown       Verdict = "Unknown"
)

// Reason：带编码的原因。Code 给程序看（稳定、测试可断言），Detail 给人看。
type Reason struct {
	Code   string
	Detail string
}

// Report：输出契约。必须带原因与观察时间。
type Report struct {
	SnapshotTakenAt time.Time
	Verdict         Verdict
	Reasons         []Reason
}
