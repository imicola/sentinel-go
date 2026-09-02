# sentinel-go

## 当前目标

使用 Go 语言启动服务器并监听来自客户端的 `GET /healthz` 请求，成功时返回 `200 OK`，其他请求方法返回 `405 Method Not Allowed`。

## 环境要求

需要安装 `go`，推荐版本 1.26.0

## 启动方式

在项目根目录执行 

```sh
go run .
```

## 接口

### GET /healthz

负责检查监听,返回`HTTP/1.1 200 OK` 和 `{"status":"ok"}`

## 手工验证

启动程序后在新的shell中依次输入

```sh
curl.exe -i http://localhost:8080/healthz

curl.exe -i -X POST http://localhost:8080/healthz

curl.exe -i http://localhost:8080/not-found
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

## 当前限制
- 仅有健康检查；
- 监听地址暂时硬编码；
- 尚未实现超时与优雅退出；
- 尚未实现自动化测试。
