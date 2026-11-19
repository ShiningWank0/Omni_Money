FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN mkdir -p /opt/omni-environment/apks \
 && apk --cache-dir /opt/omni-environment/apks upgrade \
 && apk --cache-dir /opt/omni-environment/apks add ca-certificates tzdata libcrypto3 \
 && cp /lib/apk/db/installed /opt/omni-environment/apk-installed \
 && cp /etc/apk/repositories /opt/omni-environment/repositories \
 && apk info -v > /opt/omni-environment/packages.txt \
 && find /opt/omni-environment/apks -type f -exec sha256sum {} + > /opt/omni-environment/apks.sha256
