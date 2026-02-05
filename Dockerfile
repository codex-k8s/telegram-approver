# syntax=docker/dockerfile:1.4

ARG GOLANG_IMAGE_VERSION=1.25.6-bookworm
FROM golang:${GOLANG_IMAGE_VERSION}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ENV DEBIAN_FRONTEND=noninteractive \
    TZ=UTC \
    GOPATH=/root/go \
    GOBIN=/usr/local/bin \
    PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin

RUN apt-get update -y \
  && apt-get install -y --no-install-recommends ca-certificates ffmpeg \
  && rm -rf /var/lib/apt/lists/*

ARG TELEGRAM_APPROVER_VERSION=latest
RUN GOBIN=/usr/local/bin go install github.com/codex-k8s/telegram-approver/cmd/telegram-approver@${TELEGRAM_APPROVER_VERSION}

ENTRYPOINT ["/usr/local/bin/telegram-approver"]
