# prom server 镜像（离线打包：静态二进制 + scratch，非 root）。
FROM scratch
COPY bin/prom /prom
EXPOSE 8082
USER 65534:65534
ENTRYPOINT ["/prom"]
