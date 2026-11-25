FROM ubuntu:24.04@sha256:562111b88caa79c71391c0c4d3c2114a7d5c35915c20561fd893cb1e07929e6b
ENV DEBIAN_FRONTEND=noninteractive
# This workflow creates a candidate once. Normal builds consume its digest.
# Preserve signed repository metadata and every downloaded deb, including the
# dependency closure. The immutable image also preserves all base packages.
RUN rm -f /etc/apt/apt.conf.d/docker-clean \
 && mkdir -p /opt/omni-environment/debs \
 && apt-get update \
 && apt-get -y -o Dir::Cache::archives=/opt/omni-environment/debs install \
    build-essential curl ca-certificates git tcl libssl-dev binutils file \
    libgtk-3-dev libwebkit2gtk-4.1-dev xvfb xauth dbus-x11 openbox wmctrl \
    xdotool python3 python3-gi gir1.2-atspi-2.0 at-spi2-core libatk-adaptor \
    sudo jq unzip xz-utils zip pkg-config iptables libasound2t64 libatk-bridge2.0-0t64 \
    libatspi2.0-0t64 libcups2t64 libdrm2 libgbm1 libnspr4 libnss3 \
    libwayland-client0 libxcomposite1 libxdamage1 libxfixes3 libxkbcommon0 \
    libxrandr2 fonts-noto-color-emoji fonts-unifont fonts-liberation \
    fonts-ipafont-gothic fonts-wqy-zenhei fonts-tlwg-loma-otf fonts-freefont-ttf gnupg-utils- \
 && ! command -v gpgtar >/dev/null 2>&1 \
 && dpkg-query -W -f='${binary:Package}\t${Version}\t${Architecture}\n' > /opt/omni-environment/packages.tsv \
 && cp -a /var/lib/apt/lists /opt/omni-environment/apt-lists \
 && find /opt/omni-environment/debs -type f -name '*.deb' -exec sha256sum {} + > /opt/omni-environment/debs.sha256 \
 && useradd --uid 1001 --create-home --shell /bin/bash omni-runner \
 && printf 'omni-runner ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/omni-runner \
 && chmod 0440 /etc/sudoers.d/omni-runner
