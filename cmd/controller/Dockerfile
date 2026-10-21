# controller 镜像（M2 CRD reconcile + ScaleExecutor）。构建：
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o cmd/controller/bin/controller ./cmd/controller
#   docker build -t aegis/controller:<版本> cmd/controller
FROM scratch
COPY bin/controller /controller
USER 65534:65534
ENTRYPOINT ["/controller"]
