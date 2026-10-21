# gatekeeper 镜像（R2 闸门/MCP）。构建：
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o cmd/gatekeeper/bin/gatekeeper ./cmd/gatekeeper
#   docker build -t aegis/gatekeeper:<版本> cmd/gatekeeper
FROM scratch
COPY bin/gatekeeper /gatekeeper
EXPOSE 8085 8086
USER 65534:65534
ENTRYPOINT ["/gatekeeper"]
