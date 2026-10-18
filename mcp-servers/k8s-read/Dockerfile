# k8s-read 镜像（气隙友好：离线打包，无需拉构建期基础镜像）。
# 构建产物由仓内静态编译产出（CGO_ENABLED=0），本 Dockerfile 仅打包：
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
#     -o mcp-servers/k8s-read/bin/k8s-read ./mcp-servers/k8s-read
#   docker build -t aegis/k8s-read:<版本> mcp-servers/k8s-read
#   kind load docker-image aegis/k8s-read:<版本> --name aegis
#
# FROM scratch：in-cluster client-go 用 Pod 挂载的 serviceaccount ca.crt 做 TLS，
# 不依赖系统 CA bundle，故 scratch 即可运行（气隙/最小攻击面，对齐 I11/N1 安全姿态）。
FROM scratch
COPY bin/k8s-read /k8s-read
EXPOSE 8080
# 非 root 运行（distroless nonroot uid）。
USER 65534:65534
ENTRYPOINT ["/k8s-read"]
