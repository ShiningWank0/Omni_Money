FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf
RUN mkdir -p /opt/omni-environment/apks \
 && apk --cache-dir /opt/omni-environment/apks add build-base linux-headers curl tcl openssl-dev \
    'libcrypto3>=3.5.9-r1' 'libssl3>=3.5.9-r1' 'zlib>=1.4-r0' \
 && cp /lib/apk/db/installed /opt/omni-environment/apk-installed \
 && cp /etc/apk/repositories /opt/omni-environment/repositories \
 && apk info -v > /opt/omni-environment/packages.txt \
 && find /opt/omni-environment/apks -type f -exec sha256sum {} + > /opt/omni-environment/apks.sha256
