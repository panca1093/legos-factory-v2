FROM node:22-bookworm-slim

ARG GO_VERSION=1.26.3
ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl make build-essential openssh-client \
 && rm -rf /var/lib/apt/lists/*

# TODO: uncertain — Docker publishes no checksums for static tarballs; these sha256 values were computed from a download of 27.3.1 (trust on first use), verify against a second source
RUN case "$TARGETARCH" in \
      amd64) a=x86_64; sum=9b4f6fe406e50f9085ee474c451e2bb5adb119a03591f467922d3b4e2ddf31d3 ;; \
      arm64) a=aarch64; sum=4da6a6c7502b7ab561675a5ff5ac192d9b49d76d0b8847cf17ade246122279f4 ;; \
      *) exit 1 ;; esac \
 && curl -fsSL -o /tmp/docker.tgz "https://download.docker.com/linux/static/stable/$a/docker-27.3.1.tgz" \
 && echo "$sum  /tmp/docker.tgz" | sha256sum -c - \
 && tar -xzf /tmp/docker.tgz -C /usr/local/bin --strip-components=1 docker/docker \
 && rm /tmp/docker.tgz

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz

# codex-acp bundles its @openai/codex binary (linux x64/arm64 optional deps); @zed-industries/codex-acp is deprecated
# TODO: uncertain — `opencode acp` was exercised on the 2.1.3 binary only; npm bin of @opencode/cli@2.2.2 is `opencode`, verify ACP on first image build
RUN npm i -g @agentclientprotocol/claude-agent-acp@0.99.1 @opencode/cli@2.2.2 @agentclientprotocol/codex-acp@2.5.0

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
