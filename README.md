# sentinel-go

一个以 Go 标准库为核心的小型 HTTP 服务，用于实践 API 开发、Linux 指标采集，以及容器与 Kubernetes 中的服务运行和故障诊断。

项目从可测试的 Target CRUD 起步，逐步连接 `/proc/meminfo`、Prometheus、Docker Compose 和本地 Kubernetes。当前也作为 Kubernetes 节点维护实验的示例负载，后续探索 Go 控制器中的维护协调与恢复；控制器尚未实现。

这是持续迭代的学习项目，开发过程使用了 AI 辅助教学、代码审查与文档整理。下面分别说明现有实现、实验入口和限制，便于复现与评估。

## 当前功能

| 部分 | 已有实现 | 边界 |
| --- | --- | --- |
| HTTP API | 健康检查、Target CRUD、输入校验和错误响应 | Target 是记录，不会主动探测其 address |
| 内存存储 | 每个 Store 独立持有数据、ID 和互斥锁 | 重启丢失数据，多副本之间不共享 |
| 服务生命周期 | 请求超时、监听错误处理、信号触发的优雅退出 | 监听地址固定为 `:9090`，退出等待上限 5 秒 |
| Linux 指标 | 将 MemAvailable 转为字节，通过 /metrics 暴露 Gauge | 只采集一个指标，读取失败返回 500 |
| PostgreSQL | 独立的 PostgresStore.Create/Get 及可选集成测试 | HTTP 仍使用 MemoryStore，数据库没有完整 CRUD |
| 容器与监控 | 多阶段 Docker 构建、Compose 双服务、Prometheus 配置 | 本地实验配置，未提供生产部署保障 |
| Kubernetes | Deployment、ClusterIP Service、readiness 与端口诊断清单 | 本地 kind 示例；未实现 Operator 或维护控制器 |

## 快速开始

需要 Go 1.26 或更高版本，最低版本以 [go.mod](go.mod) 为准。完整指标路径需要 Linux、WSL 或 Linux 容器。Windows 原生运行可以使用 HTTP CRUD，但通常没有 `/proc/meminfo`，因此 `/metrics` 会返回 500。

```sh
git clone https://github.com/imicola/sentinel-go.git
cd sentinel-go
go run .
```

在另一终端检查服务：

```sh
curl -i http://localhost:9090/healthz
curl -i http://localhost:9090/target
curl -i http://localhost:9090/metrics
```

PowerShell 中可使用 `curl.exe`。健康检查返回 200 和 `{"status":"ok"}`，新进程中的 Target 列表返回 `[]`。健康检查只验证应用能够处理该请求，不检查数据库或指标采集是否成功。

服务监听全部网卡的 9090 端口，尚无认证、授权或 TLS，请在本地或受控实验环境中运行。

## HTTP 接口

| 方法 | 路径 | 行为 | 成功状态 |
| --- | --- | --- | --- |
| GET | /healthz | 返回应用健康响应 | 200 |
| GET | /metrics | 返回 Prometheus 文本格式的内存指标 | 200 |
| GET | /target | 列出当前实例中的 Target | 200 |
| GET | /target/{id} | 查询 Target | 200 |
| POST | /target | 创建 Target | 200 |
| PUT | /target/{id} | 替换 Target 的 name 与 address | 200 |
| DELETE | /target/{id} | 删除 Target，无响应正文 | 204 |

POST 与 PUT 接收以下 JSON；两个字段均不能为空，未知字段会被拒绝：

```json
{"name":"demo","address":"127.0.0.1:8080"}
```

创建与查询响应示例：

```json
{"id":1,"name":"demo","address":"127.0.0.1:8080"}
```

非法输入通常返回 400，缺失的 Target 返回 404，不支持的方法返回 405。错误正文目前没有统一 JSON 格式。POST 当前返回 200，尚未改为 201。

## Docker Compose 与 Prometheus

需要 Docker 的 Linux 容器模式和 Docker Compose。在项目根目录执行，镜像标签与现有清单保持一致：

```sh
docker build -t sentinel-go:unit-20261003 .
docker compose -f deploy/compose-unit/compose.yaml up -d
docker compose -f deploy/compose-unit/compose.yaml ps
```

| 入口 | 地址 |
| --- | --- |
| 应用健康检查 | http://127.0.0.1:29090/healthz |
| 应用指标 | http://127.0.0.1:29090/metrics |
| Prometheus | http://127.0.0.1:29091 |

在 Prometheus 查询 `up{job="sentinel-compose"}` 查看抓取状态，查询 `sentinel_memory_available_bytes` 查看指标。Compose 内部抓取地址为 `sentinel-go:9090`，与宿主机发布端口不同。

可以停止应用观察 up 的变化，再启动观察恢复：

```sh
docker compose -f deploy/compose-unit/compose.yaml stop sentinel-go
docker compose -f deploy/compose-unit/compose.yaml start sentinel-go
```

Prometheus 数据保存在命名卷中。`docker compose -f deploy/compose-unit/compose.yaml down` 清理容器和网络，保留数据卷；附加 `-v` 会删除该卷及其中的监控历史。

根目录的 [prometheus.yml](prometheus.yml) 使用 localhost:9090，适合 Prometheus 与应用共享本机网络的场景。Compose 使用其目录内的配置，两种配置不能直接互换。

## 本地 Kubernetes

需要 Docker、kind 和 kubectl。以下创建独立实验集群，显式指定 context，避免将清单应用到其他集群：

```sh
kind create cluster --name sentinel-demo
docker build -t sentinel-go:unit-20261003 .
kind load docker-image sentinel-go:unit-20261003 --name sentinel-demo
kubectl --context kind-sentinel-demo create namespace w41-practice
kubectl --context kind-sentinel-demo apply -f deploy/k8s-unit/deployment.yaml
kubectl --context kind-sentinel-demo apply -f deploy/k8s-unit/services.yaml
kubectl --context kind-sentinel-demo -n w41-practice rollout status deployment/sentinel-deployment
kubectl --context kind-sentinel-demo -n w41-practice port-forward service/sentinel-service 29090:9090
```

保持端口转发运行，在另一终端访问 `http://127.0.0.1:29090/healthz` 和 /metrics。如果 Compose 正占用 29090，可将端口转发的本地端口改为 29092。

w41-practice 是现有清单固定的实验 namespace。基础 Deployment 没有探针；deployment-probe-test.yaml 与 service-probe-test.yaml 是独立的 readiness 实验，service-diag-test.yaml 用于修改 targetPort 并观察失败与恢复。初次部署只应用上面的两个基础清单。

指标读取应用运行环境中的 /proc/meminfo。在容器中它不代表 Target 地址的内存，也不能直接当作 Pod 的剩余内存配额或 Kubernetes 节点资源报告。

## 测试

普通测试无需数据库、Prometheus 或 Kubernetes：

```sh
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
```

测试覆盖 HTTP CRUD 与输入错误、并发创建、Store 实例隔离、MemAvailable 的解析错误与溢出、指标响应及 context 实验。race 检查需要平台支持的 C 编译器。

PostgreSQL 测试默认跳过。要运行它们，先准备专用测试数据库，并在该空库执行 [db/schema.sql](db/schema.sql) 创建 targets 表。测试会创建和删除数据，请使用可丢弃的测试库。PowerShell 示例：

```powershell
$env:DATABASE_URL = "postgres://USER:PASSWORD@localhost:5432/TEST_DB?sslmode=disable"
$env:RUN_POSTGRES_TESTS = "1"
go test -v -count=1 .
```

替换占位符，不要提交真实连接凭据。sslmode=disable 仅用于本地实验。服务本身不会因为设置 DATABASE_URL 而切换到 PostgreSQL。

## 代码入口

| 路径 | 内容 |
| --- | --- |
| main.go | HTTP 路由、Handler、超时与退出流程 |
| store.go / store_test.go | 内存状态与行为契约 |
| meminfo.go / metrics.go | Linux 内存解析与指标输出 |
| storedb.go / storedb_test.go | PostgreSQL Create/Get 与集成测试 |
| experiments/ | context 和 database/sql 的独立实验 |
| deploy/compose-unit/ | 应用与 Prometheus 的双容器实验 |
| deploy/k8s-unit/ | 服务部署、就绪与端口诊断实验 |

## 后续方向

继续以这个服务作为可观察的实验负载，探索 Kubernetes 节点维护、Eviction、PDB 与替代副本恢复，再实现有范围约束的 Go 维护协调控制器。重点是可重试动作、状态恢复与变更所有权。这些属于后续目标，当前仓库不提供相应控制器功能。

## 许可证

本项目采用 [MIT License](LICENSE)。

