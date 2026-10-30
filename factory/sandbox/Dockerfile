FROM node:22-bookworm-slim

ARG GO_VERSION=1.26.3
ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl make build-essential openssh-client \
 && rm -rf /var/lib/apt/lists/*

RUN case "$TARGETARCH" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; *) exit 1 ;; esac \
 && curl -fsSL "https://download.docker.com/linux/static/stable/$a/docker-27.3.1.tgz" | tar -xz -C /usr/local/bin --strip-components=1 docker/docker

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz

# TODO: uncertain — @opencode/cli assumed to be the OpenCode v2 CLI (npm latest 2.2.2; opencode-ai is 1.x), verify its ACP entrypoint on first image build
RUN npm i -g @agentclientprotocol/claude-agent-acp@0.99.1 @opencode/cli@2.2.2

ENV PATH=/usr/local/go/bin:$PATH \
    HOME=/ws/home \
    GOCACHE=/cache/go \
    GOMODCACHE=/cache/gomod \
    npm_config_cache=/cache/npm

# Pre-created and owned so fresh named volumes inherit usable ownership.
RUN useradd -m -u 1001 -d /ws/home factory \
 && mkdir -p /ws /cache/go /cache/gomod /cache/npm \
 && chown -R factory:factory /ws /cache

USER factory
WORKDIR /ws
CMD ["sleep", "infinity"]
