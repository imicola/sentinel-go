# sentinel-go

## 当前目标

使用 Go 标准库实现带内存 Target CRUD、服务器超时配置和优雅退出的 HTTP 服务，并通过自动化测试与 race 检查验证主要行为。

## 环境要求

需要安装 `go`，推荐版本 1.27.1

## 启动方式

在项目根目录执行 

```sh
go run .
```

## 接口

### GET /healthz

负责检查监听,返回`HTTP/1.1 200 OK` 和 `{"status":"ok"}`

### GET /target

负责列出所有 target 数据

### GET /target/{id}

负责查询某一个 target 数据

### POST /target

负责新增 target 数据,请求body需要包含 `name` 和 `address`

### PUT /target/{id}

负责修改某一个 target 数据

### DELETE /target/{id}

负责删除某一个 target 数据


## 手工验证

启动程序后在新的shell中依次输入

```sh
curl.exe -i http://localhost:9090/healthz

curl.exe -i -X POST http://localhost:9090/healthz

curl.exe -i http://localhost:9090/not-found
```

程序应该依次返回

```sh
HTTP/1.1 200 OK
Content-Type: application/json
Date: ~
Content-Length: 15

{"status":"ok"}

---

HTTP/1.1 405 Method Not Allowed
Date: ~
Content-Length: 0

---

HTTP/1.1 404 Not Found
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff
Date: ~
Content-Length: 19

```

## 测试

在终端中输入

```sh
go test -count=1 ./...
go test -race -count=1 ./...
```

以启动自动测试，自动测试主要实现了

- `healthz` 正常返回
- 对 `healthz` 非法传入方法
- 针对业务流程的 CRUD 测试
- 针对 `POST` 接口传入非法json的测试
- 针对 `POST` 接口传入缺失json的测试
- 服务状态中空列表与创建两条后的列表测试
- 对 POST 的并发测试

## 当前限制
- 监听地址暂时硬编码；

