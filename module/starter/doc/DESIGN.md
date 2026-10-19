# DESIGN FOR STARTER

[write]：imicola
[time]: 2026-10-18 20:44 Asia/Shanghai

## 模块设计

这是一份以 Go 驱动的，针对 K8s 集群的维护前只读预检工具，作用是在进行 drain 驱逐集群 Node 前对集群的状态进行查询生成快照，并且根据快照以下方规定的 [规则清单](##规则清单) 做出基础判断的模块程序，下面简单说明设计思路与方法函数作用



## 规则清单

按课程0015，将集群 drain 前状态划分为 4 层

- Unknow  	     -> 读取到错误

- Blocked   	   -> 明确堵塞

- NeedReview	   -> 交人工
- ObservedReady	-> 本次通过

按结论优先级明确划分为 `Unknown > Blocked > NeedsReview > ObservedReady`，下方规则表展示了各种情况与结论的对应关系，当然也可以在 [analyze.go](../analyze.go) 前的注释中看到



| R0   | `Errors` 非空                                | Unknown       | `read-error/<step>`                    |
| ---- | -------------------------------------------- | ------------- | -------------------------------------- |
| R1   | `Node.Ready` 为 false                        | Blocked       | `node-not-ready`                       |
| R2   | 就绪副本 < 期望副本                          | Blocked       | `target-not-ready`                     |
| R3   | `Node.Unschedulable` 为 true                 | NeedsReview   | `node-already-cordoned`                |
| R4   | 节点上有 bare / other 分类的 Pod             | NeedsReview   | `bare-pod` / `other-workload`          |
| R5   | 节点上的 Pod 用了 emptyDir/hostPath/PVC      | NeedsReview   | `local-data` / `pvc-volume`            |
| R6   | 圈中可驱逐Pod，当前`DisruptionsAllowed <= 0` | Blocked       | `pdb-budget-zero`                      |
| R6a  | 当前余量为正，但小于节点上匹配的批次数       | NeedsReview   | `pdb-sequential-review`                |
| R6c  | 同一可驱逐Pod匹配多份PDB                     | Blocked       | `pdb-overlap`                          |
| R6b  | PDB 的 selector 用了 matchExpressions        | NeedsReview   | `pdb-selector-unsupported`             |
| 信息 | DaemonSet / mirror Pod                       | 不改变结论    | `skipped-daemonset` / `skipped-mirror` |
| R7   | 一条规则都没触发                             | ObservedReady | `all-rules-passed`（Detail 写明边界）  |



## 模块架构与设计思路

模块在`main.go`中装配连接集群，使用 `reader.go` 读取某一个时刻下的 `Snapshot` 快照,再将 `Snapshot` 交由 `abalyze.go` 做状态分析得出 `Report`，随后交由 `output.go` 中 `RunPreflight` 函数做序列化和退出码返回，下面将对重要的函数设计和结构体设计进行说明



### `type.go`

`type.go` 是模块的数据结构基础，其中主要的数据结构是 `Snapshot` 和 `Report`

```go
type Snapshot struct {
	TakenAt    time.Time
	Node       NodeState
	Deployment DeploymentState
	Pods       []PodState // 目标节点上全部命名空间的 Pod
	PDBs       []PDBState // 与这些 Pod 匹配的 PDB（按命名空间列出后按标签匹配）
	Errors     []ReadError
}

type Report struct {
	SnapshotTakenAt time.Time
	Verdict         Verdict
	Reasons         []Reason
}
```



在 `snapshot` 中，Pods,PDBs 等一个节点中可能有多个对象的内容都使用 *slide struct* 设计，主要由一个节点中 `时间戳 + 节点状态 + Deployment 对象状态 + Pods + PDB状态 + Error列表` 构成



对于 `Report` ，主要由 `时间戳 + 判定状态 + 原因` 构成



### `reader.go`

`reader.go` 主要用于用于提取某一个时刻集群的状态，主要函数是 `ReadSnapshot`,作为 `KubeReader` 的成员函数



函数中有很多类似

```go
	if err != nil {
		fmt.Println("读取目标 Node 失败:", err)
		s.Errors = append(s.Errors, ReadError{Step: "read-node", Message: err.Error()})
	} else {
		// process
	}
```

的结构，主要原因是我们不能因为某单个问题放弃后面的所有内容，所以就算读取到了 *Error*，处理方式应该是收集到 `Snapshot.Errors` 中并且跳过有关这段的逻辑，继续向下收集



有设计点需要说明:

1. 在对 `Pods` 状态进行采集的时候，`Pods` 的 `Class` 属性需要注意，这里使用 `getPodClass` 获取
   1. 首先需要判断是否属于 `mirror/daemonset/bare` 类型
   2. 然后根据 `pod -> rs -> dep` 链条逐级往上找，只有链条都通后，才能判定是 `PodClassTarget`



### `Analyze.go`

`Analyze.go` 的主要职责就是根据生成的 `Shapshot` 来生成 `Report` 



### 一些说明与限制

- `snapshot` 中的时刻只表示记录的时刻，而 `Snapshot`本身也一次采集汇总，不能表示所有对象在同一瞬间的原子快照
- `ObservedReady`表示采集到的事实满足上述的规则，但是不能保证随后驱逐一定成功或业务不中断
- 程序只读并报告，不执行 cordon、Eviction 或 uncordon



