# logs server 镜像（静态二进制 + scratch，非 root）。
FROM scratch
COPY bin/logs /logs
EXPOSE 8083
USER 65534:65534
ENTRYPOINT ["/logs"]
