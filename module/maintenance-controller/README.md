# maintenance-controller

一个基于 Go 和 controller-runtime 的受限 Kubernetes 节点维护控制器：读取 MaintenancePlan，预检节点、负载和 PDB，取得本申请的 cordon 所有权，在显式许可下通过 Eviction API 疏散指定 Deployment，并观察替代副本就绪。

API Server 负责准入与持久化，kubelet 负责终止 Pod，ReplicaSet 和调度器负责补副本与调度；本控制器负责判断、有限动作和进度报告。

## 支持范围

当前按单申请、单 worker、单控制器进程/副本运行，支持明确指定的无状态 Deployment。不支持任意负载或有状态/本地数据迁移；未知归属、其他不受支持负载及数据卷等会阻塞，规则见 [decide.go](./decide.go)。

默认只执行受控 cordon；显式设置 allowEviction=true 才进入疏散。驱逐遵守 PDB，不直接删除业务 Pod 绕过保护。持久进度包含固定截止与动作冷却，重启后重新读取并核对现场。

WorkloadEvacuated 只表示受支持负载满足疏散完成条件，不表示节点维护已完成或可以直接关机。暂未实现自动 uncordon、取消清理、多节点预算、leader election 或 HA；不承诺零中断或生产可用。

## 环境与连接

需要 Go（版本见 [go.mod](./go.mod)）、kubectl 和已有的隔离 Kubernetes 集群；镜像部署还需要 Docker，下文使用 kind 示例。除构建检查与本地运行外，命令均从 sentinel-go 仓库根目录执行。

示例使用 kind 集群 work-maintenance、命名空间 maintenance-lab、目标节点 work-maintenance-worker 和业务 Deployment maintenance-sentinel。控制器安装清单不创建集群或业务负载；先准备命名空间、业务镜像、Deployment 和匹配它的 PDB，按实际环境替换名称。

每个新 PowerShell 终端设置：

~~~powershell
$env:KUBECONFIG = "<本机集群配置文件路径>"
$ctx = "kind-work-maintenance"
$ns = "maintenance-lab"
$node = "work-maintenance-worker"
$plan = "maintenance-example"
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = [Console]::OutputEncoding
~~~

KUBECONFIG 选择连接文件，--context 明确选择上下文。安装 CRD/RBAC 使用安装者权限；运行 Pod 使用专用 ServiceAccount，不挂载管理员 kubeconfig。

## 构建与检查

在 module/maintenance-controller 模块目录执行：

~~~powershell
$env:GOWORK = "off"
go build -o maintenance-controller.exe .
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
~~~

独立模块不依赖上层 go.work；race 需要可用的 C 编译工具链。

回到仓库根目录构建并导入镜像，标签与部署清单一致：

~~~powershell
docker build -f module/maintenance-controller/dockerfile.controller -t maintenance-controller:w45 module/maintenance-controller
kind load docker-image maintenance-controller:w45 --name work-maintenance
~~~

镜像以 UID/GID 65532 运行，[.dockerignore](./.dockerignore) 限制构建输入，不应放入真实凭据。非 kind 集群需自行发布镜像并修改镜像地址和拉取策略。

## 集群内部署

启动前核对目标集群、节点 UID/Ready/调度状态、业务 Pod 归属与 PDB，确认没有遗留申请或另一个本地/Pod 控制器。记录原业务副本数与 PDB 配置，供恢复时对照。

当前 [Deployment](./k8s/deployment-controller.yaml) 使用单副本、Recreate 更新策略，要求节点带 maintenance.kind/group=b 标签；示例维护目标在 a 组。控制器应运行在维护目标之外。Recreate 不提供 HA，也不保证手动删 Pod 时没有新旧进程重叠；明确停机使用缩到 0 并等待退出。

先安装 API 和权限，检查现场后再启动：

~~~powershell
kubectl --context $ctx apply -f module/maintenance-controller/k8s/maintenance-crd.yaml
kubectl --context $ctx wait --for=condition=Established crd/maintenanceplans.maintenance.studymate.io --timeout=60s
kubectl --context $ctx apply -f module/maintenance-controller/k8s/rbac-controller.yaml
kubectl --context $ctx get nodes -o wide
kubectl --context $ctx -n $ns get "deployment,pdb,pods,mplan" -o wide

# 确认现场后执行。replicas=1，再次 apply 也会启动已停止的控制器。
kubectl --context $ctx apply -f module/maintenance-controller/k8s/deployment-controller.yaml
kubectl --context $ctx -n $ns rollout status deployment/maintenance-controller --timeout=60s
kubectl --context $ctx -n $ns get pods -l app=maintenance-controller -o wide
~~~

--mode in-cluster 选择连接配置，serviceAccountName 指定身份。[RBAC](./k8s/rbac-controller.yaml) 提供缓存所需的跨命名空间读取和 Node update，status update 与 pods/eviction create 限于 maintenance-lab。更换命名空间时同步调整权限与部署配置；can-i 通过不代替实际启动验证。

也可在本模块目录以前台进程运行，与 Pod 控制器二选一：

~~~powershell
go run . --mode local --kubeconfig $env:KUBECONFIG --context $ctx
~~~

local 必须同时给 kubeconfig 和 context；in-cluster 拒绝混用这两个参数，配置错误会启动失败。

## 发起维护

以下示例先关闭驱逐许可，预检通过后仅 cordon。目标节点应确实有受支持业务 Pod；副本健康和 PDB 应允许维护所需的中断。

~~~powershell
@"
apiVersion: maintenance.studymate.io/v1alpha1
kind: MaintenancePlan
metadata:
  name: $plan
  namespace: $ns
spec:
  nodeName: $node
  targetRef:
    namespace: $ns
    name: maintenance-sentinel
  allowEviction: false
  drainTimeoutSeconds: 600
"@ | kubectl --context $ctx create -f -

# 保存创建时的身份，用于归还核对；跨终端时也应保留。
$planUid = kubectl --context $ctx -n $ns get mplan $plan -o jsonpath='{.metadata.uid}'
kubectl --context $ctx -n $ns get mplan $plan -o yaml
~~~

drainTimeoutSeconds 是进入疏散阶段后的整轮固定时间窗，600 秒只是示例。到达 Cordoned 后重新核对申请所有权、Pod 归属和预算，再按维护决定开放许可：

~~~powershell
kubectl --context $ctx get node $node -o yaml
kubectl --context $ctx -n $ns get pdb
kubectl --context $ctx -n $ns get pods -l app=maintenance-sentinel -o wide

# 确认允许执行后才提交。
kubectl --context $ctx -n $ns patch mplan $plan --type merge -p '{"spec":{"allowEviction":true}}'
~~~

进度开始后变更申请意图或目标对象身份会阻塞交人工处理。删除申请不撤销已发生的 cordon 或驱逐。

## 观察与排障

~~~powershell
kubectl --context $ctx -n $ns get mplan $plan -o yaml
kubectl --context $ctx -n $ns get pods -l app=maintenance-sentinel -o wide
kubectl --context $ctx -n $ns get pdb maintenance-pdb
kubectl --context $ctx -n $ns logs deployment/maintenance-controller --timestamps --tail=100
~~~

nextActionAt 是新驱逐动作的最早允许时刻，deadline 是整轮固定截止。事件可提前唤醒协调，但不能绕过冷却；到期后仍须重读现场。完成观察不受动作冷却阻挡。

区分请求发出、API 受理、旧 UID 消失和替代副本 Ready。activePod 在调用前保存，单独存在不证明受理；deletionTimestamp 表示已进入删除流程，其时间可包含优雅退出宽限期，不是精确终止开始时间。请求次数不能由协调次数或 status 更新次数推断。

| reason / 现象 | 检查与处理 |
| --- | --- |
| pdb-budget-zero | 预检读到预算为 0，尚未发出驱逐请求。查 disruptionsAllowed、业务副本 Ready 和保护要求。 |
| pdb-budget-exhausted | 请求已被识别为 PDB 拒绝。对照调用日志、当前预算与 nextActionAt；冷却未到期等待，到期后重新判断，不保证成功。普通 429 不一定来自 PDB。 |
| old-pod-terminating / replacement-not-ready | 查旧 Pod UID、终止状态和替代副本就绪情况；正常推进时等待，不靠重启“修复”。 |
| eviction-forbidden | 查专用身份对目标命名空间 pods/eviction 的 create 权限，修复权限后再处理。 |
| foreign-marker / cordon-ownership-lost | 对照节点标记与申请 UID，协调所有权，不覆盖别人的标记或直接解封。 |
| drain-timeout-exceeded | 已过固定截止。核对现场后决定放弃或归还，不通过重启刷新时间窗。 |

PDB 配置变更后，确认 status.observedGeneration 追上 metadata.generation，再判断预算。预算仍为 0 时先检查副本健康与保护要求，不为让申请通过而直接降低保护。

日志查询需要存在可读取的控制器 Pod。停止前保存日志；缩到 0 后查看已保存文件，不为查询历史日志启动控制器。当前源码部分拒绝分支仍写“本轮不请求”，实际位于调用返回后，应结合 reason 和代码位置判读。

## 停止与人工归还

先保存申请与日志，再停止所有相关协调进程。本地进程用 Ctrl+C，Pod 控制器用：

~~~powershell
kubectl --context $ctx -n $ns scale deployment/maintenance-controller --replicas=0
kubectl --context $ctx -n $ns wait --for=delete pod -l app=maintenance-controller --timeout=60s
~~~

停止进程不自动解封节点。下面要求保留创建时的 $planUid，并确认本地控制器也已退出。未取得 cordon 所有权、申请已删或身份不符时，先人工核对，不仅凭节点名称执行 uncordon。

~~~powershell
& {
    function K {
        $result = kubectl --context $ctx --request-timeout=5s @args
        if ($LASTEXITCODE -ne 0) { throw "命令失败：$args" }
        $result
    }
    if ([string]::IsNullOrWhiteSpace([string]$planUid)) {
        throw "缺少创建时记录的申请 UID。"
    }
    $controller = K -n $ns get deployment maintenance-controller -o json | ConvertFrom-Json
    $pods = K -n $ns get pods -l app=maintenance-controller -o json | ConvertFrom-Json
    if ($controller.spec.replicas -ne 0 -or $pods.items.Count -ne 0) {
        throw "Pod 控制器尚未完全停止。"
    }
    $p = K -n $ns get mplan $plan -o json | ConvertFrom-Json
    $n = K get node $node -o json | ConvertFrom-Json
    if ($p.metadata.uid -ne $planUid -or $p.spec.nodeName -ne $node -or
        -not $p.status.cordonIntent.nodeUID -or
        $n.metadata.uid -ne $p.status.cordonIntent.nodeUID -or
        $n.metadata.annotations.'maintenance.studymate.io/cordon-plan-uid' -ne $planUid) {
        throw "申请、节点身份或所有权不一致，停止归还。"
    }
    K uncordon $node
    K annotate node $node maintenance.studymate.io/cordon-plan-uid-
    K -n $ns delete mplan $plan
    K get node $node -o yaml
}
~~~

按事先记录恢复本次修改过的业务副本与 PDB，没有修改的不必重设。最终确认：申请已删除、控制器为 0、节点可调度且本申请标记为空、业务就绪与预算符合恢复目标。任何命令失败先检查现场，不盲目重跑整段。

## 代码导航

| 文件 | 职责 |
| --- | --- |
| [main.go](./main.go) | 显式连接模式、Manager 和 Eviction 客户端装配 |
| [facts.go](./facts.go) | 节点负载、Deployment 身份链和 PDB 事实 |
| [decide.go](./decide.go) | 预检、动作选择与完成判断 |
| [reconciler.go](./reconciler.go) | 持久进度、冷却、错误处理和报告 |
| [cordon.go](./cordon.go) / [evictor.go](./evictor.go) | Node 更新与携 UID 前置的驱逐调用 |
| [plan_type.go](./plan_type.go) / [CRD](./k8s/maintenance-crd.yaml) | API 类型与持久字段 schema |
| [cooldown_test.go](./cooldown_test.go) / [drain_test.go](./drain_test.go) | 冷却、完成和恢复回归；fake 不模拟真实 PDB 准入 |
