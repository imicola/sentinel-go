# maintenance-controller

一个有明确支持边界的Kubernetes节点维护学习项目。使用Go与controller-runtime：观察MaintenancePlan和集群事实，受控cordon，并在显式许可下通过Eviction API疏散受支持的Deployment负载。API Server、PDB准入、调度和副本补齐由Kubernetes承担。

状态核对：2026-11-05，依据当前源码与截至11月4日的工程记录。本文件由AI整理，核心实现来自本人在教学、示例和审查支持下的实践；文档准备不构成独立部署、第三人复现或生产经验。

## 当前范围与证据

单申请、单worker、单进程/副本、受限无状态Deployment。未实现自动uncordon、维护完成确认、取消清理、多节点预算或HA。不支持有状态/本地数据迁移；不通过直接删除Pod绕过PDB。

| 范围 | 已有证据 | 尚未证明 |
| --- | --- | --- |
| 受控cordon、PDB保护与恢复 | 指导下kind真实正常疏散、PDB拒绝后恢复、人工归还 | 生产维护、零中断、任意工作负载 |
| 持久冷却、截止与完成优先 | 离线回归、错误副本对照、CRD字段本地一致性 | 新字段在真实API中持久化及实际请求频率 |
| 独立模块、镜像、双入口 | 格式/vet/普通/race于11月4日通过；镜像构建和入口错误行为由本人操作确认 | 专用Pod身份部署、镜像导入kind、信号退出运行观察 |
| 重启恢复 | 离线新协调器读持久进度；早期cordon真实停止/重启认领 | 普通Eviction受理后的真实进程窗口 |
| 操作说明 | 当前模块入口及限制已整理 | 完整SA/RBAC/Deployment与第三人照文运行 |

这里的WorkloadEvacuated仅表示受支持业务范围的疏散事实，不表示节点可关机、维护已完成或所有负载都已清空。未启用allowEviction的申请止于Cordoned。

## 代码入口

| 文件 | 作用 |
| --- | --- |
| [main.go](./main.go) | 显式运行模式、连接配置、Manager与typed Eviction客户端装配 |
| [facts.go](./facts.go) | 跨namespace节点负载与Deployment归属事实采集 |
| [decide.go](./decide.go) | 预检、优先级、下一动作与完成判断 |
| [reconciler.go](./reconciler.go) | 持久进度、动作冷却、失败返回与状态报告 |
| [cordon.go](./cordon.go) | 同次Node Update写调度资格与所有权标记 |
| [evictor.go](./evictor.go) | 携UID前置的Eviction及只计数/回放的替身 |
| [plan_type.go](./plan_type.go) | API类型、常量与深拷贝 |
| [k8s/maintenance-crd.yaml](./k8s/maintenance-crd.yaml) | 当前CRD/schema，包含status.evictionProgress.nextActionAt |
| [cooldown_test.go](./cooldown_test.go) | 提前协调、重建、到期/截止、保存顺序及完成优先回归 |
| [drain_test.go](./drain_test.go) | 疏散、拒绝、完成与恢复行为；fake不模拟真实PDB准入 |

## 构建与离线检查

在本模块目录执行，Go版本以[go.mod](./go.mod)为准。独立模式不依赖上层go.work；PowerShell当前进程可先设置：

```powershell
$env:GOWORK = 'off'
go build -o maintenance-controller.exe .
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

race需要可用的本地C编译工具链。Work学习环境复用已有共享构建缓存，不新建一套独立缓存。上面是可复用命令，本次文档修订未重新执行Go检查，复用11月4日同范围证据。

镜像构建上下文就是本模块目录：

```powershell
docker build -f dockerfile.controller -t maintenance-controller:local .
```

[dockerfile.controller](./dockerfile.controller)构建Linux程序，以65532:65532用户运行；[.dockerignore](./.dockerignore)限定构建输入。不要把真实kubeconfig放进镜像。标签local只是示例，不代表已导入任何集群。

## 本地入口与启动前提

控制器能够写Node、发起驱逐，因此先完成隔离实验环境与本轮对象核对，再启动。至少确认显式context、集群/CRD、目标Node UID/Ready/调度状态、申请UID/目标Deployment及Pod归属、PDB状态，并确保没有另一个本地或Pod控制器同时处理同一申请。

启动模板：

```powershell
go run . --mode local --kubeconfig '<kubeconfig路径>' --context '<已核实的context>'
```

local必须同时给kubeconfig与context；缺模式、缺配置或错误context明确失败。main顶部仍保留早期课程注释，以上显式mode和本目录CRD路径以当前实际代码为准。

当前CRD的spec包含nodeName、targetRef.namespace、targetRef.name、可选allowEviction和drainTimeoutSeconds。默认allowEviction=false；默认疏散期限是项目示例的600秒，并非通用推荐。不要在未完成身份与对象核对时新建带驱逐许可的申请。

## 集群内入口尚待组装与运行验证

Pod参数为`--mode in-cluster`，从Pod提供的API连接身份取配置，拒绝混用local参数。普通容器没有Pod身份，不能靠这个参数自动连入集群。

目前k8s目录只有CRD，没有可宣称已验收的ServiceAccount/RBAC/Deployment。后续部署需要：

- 专用ServiceAccount；默认Manager缓存所需的跨namespace读取与受限写权限分别配置。
- 对maintenanceplans/status的update、对目标Node的update、对目标namespace的core pods/eviction的create。读取面还涉及Node、Pod、Deployment、ReplicaSet与PDB。
- 单副本且更新不重叠，控制器位于维护目标之外；安装者权限与运行身份分开，不能挂管理员kubeconfig代替Pod权限。

权限检查需要与实际Manager启动和动作验证相互对照。can-i通过不证明缓存已同步或驱逐已完成。现有本地索引/反复Get不自动证明服务端最新事实。

## 运行观察与人工归还

观察时区分四件事：请求是否发出、API是否受理、旧UID是否消失、替代Pod是否在目标之外Ready。持久进度用于恢复判断，不是动作成功凭据。报告中的固定deadline与nextActionAt也不是同一个约束。

429后应保留旧Pod，条件恢复再推进；实际请求频率必须由请求边界或API审计的时间证据验证，不能只数Reconcile日志。真实nextActionAt是否被schema保留，需要从API重读，离线测试不能代签。

结束时先停本轮所有控制器。Ctrl+C或终止Pod只停进程，不自动解封节点或清理申请。对照本轮记录确认Node UID与所有权标记`maintenance.studymate.io/cordon-plan-uid`的值等于本申请UID，再人工归还属于本轮的调度/标记、PDB、副本和申请变更；发现身份/所有权不符就先诊断。删除申请本身不触发自动归还。

完整运行命令、预期输出和归还实录应在专用身份与环境核实后补入；本说明没有把未验证的集群内路径预填为成功。
