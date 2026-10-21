# gitops server 镜像（R2 PR 通道，M2 T2.10）。
# 构建产物由仓内静态编译产出：
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
#     -o mcp-servers/gitops/bin/gitops ./mcp-servers/gitops
#   docker build -t aegis/gitops:<版本> mcp-servers/gitops
FROM scratch
COPY bin/gitops /gitops
EXPOSE 8087
USER 65534:65534
ENTRYPOINT ["/gitops"]
