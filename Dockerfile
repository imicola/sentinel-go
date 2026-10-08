# stage 1 构建builder
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/sentinel-go .

# stage 2  生产环境构建
FROM debian:bookworm-slim
WORKDIR /app
COPY --from=builder /bin/sentinel-go /app/sentinel-go
EXPOSE 9090
USER 65532:65532
ENTRYPOINT [ "/app/sentinel-go" ]
