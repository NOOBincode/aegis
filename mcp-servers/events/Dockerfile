# events server 镜像（静态二进制 + scratch，非 root）。
FROM scratch
COPY bin/events /events
EXPOSE 8084
USER 65534:65534
ENTRYPOINT ["/events"]
