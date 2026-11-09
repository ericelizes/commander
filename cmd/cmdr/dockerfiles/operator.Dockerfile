# operator.Dockerfile — code-type operator container image.
#
# Built + tagged as commander/operator:latest. Spawned by internal/spawn for
# every Lead-view "Enter on Task" against a code-type project.
#
# Per share/contracts.md "Decision 6 — bd binary inside operator container":
# do NOT volume-mount the host's macOS bd/am binaries — they are Mach-O and
# fail on Linux. We install Linux-native binaries at build time below.

FROM node:20-alpine

# Runtime deps:
#   bash, jq, curl       — operator scripts and contract helpers
#   git                  — operator commits to its worktree
#   ca-certificates ssl  — TLS for opencode + bd/am downloads
#   tmux                 — knowledge-type operators reuse this image too
RUN apk add --no-cache bash git jq curl ca-certificates openssl tmux

# opencode runtime (npm package is `opencode-ai`, binary is `opencode`).
RUN npm install -g opencode-ai

# Linux bd binary (beads_rust). Pinned per share/contracts.md.
# TODO(U10/U11): verify the canonical release URL once a Linux build is published.
# If the URL below 404s at build time, fall back to a build stage that
# `cargo install`s beads_rust. Documented as TODO so this Dockerfile can be
# committed without blocking U9 logic.
ARG BD_VERSION=1.0.2
RUN curl -fsSL "https://github.com/Dicklesworthstone/beads_rust/releases/download/v${BD_VERSION}/br-linux-x86_64" -o /usr/local/bin/br \
    && chmod +x /usr/local/bin/br \
    && ln -s /usr/local/bin/br /usr/local/bin/bd

# Linux am binary (mcp_agent_mail_rust). Same TODO note as bd above.
ARG AM_VERSION=0.2.47
RUN curl -fsSL "https://github.com/Dicklesworthstone/mcp_agent_mail_rust/releases/download/v${AM_VERSION}/am-linux-x86_64" -o /usr/local/bin/am \
    && chmod +x /usr/local/bin/am

WORKDIR /workspace

# Container is invoked with an explicit cmd from internal/docker.Spawn — see
# internal/spawn.buildSpawnOptions. The default ENTRYPOINT lets `docker run
# image bash -lc "..."` work for ad-hoc debugging.
ENTRYPOINT ["bash", "-lc"]
