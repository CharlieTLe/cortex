FROM       alpine:3.19
ARG TARGETARCH

RUN        apk add --no-cache ca-certificates
COPY       migrations /migrations/
COPY       cortex-$TARGETARCH /bin/cortex
EXPOSE     80
ENTRYPOINT [ "/bin/cortex" ]

ARG revision
LABEL org.opencontainers.image.title="cortex" \
      org.opencontainers.image.source="https://github.com/cortexproject/cortex/tree/master/cmd/cortex" \
      org.opencontainers.image.revision="${revision}"
