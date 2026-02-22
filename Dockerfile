# syntax=docker/dockerfile:1.7

FROM golang:1.24-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/a2 ./cmd/aagent

FROM alpine:3.21

RUN apk add --no-cache \
    bash \
    ca-certificates \
    ffmpeg \
    git \
    ripgrep \
    tini

RUN addgroup -S -g 10001 aagent \
    && adduser -S -D -u 10001 -G aagent -h /home/aagent aagent \
    && mkdir -p /workspace /data /data/.config/aagent \
    && chown -R aagent:aagent /workspace /data /home/aagent

COPY --from=builder /out/a2 /usr/local/bin/a2

ENV HOME=/data \
    AAGENT_DATA_PATH=/data

WORKDIR /workspace
USER aagent

EXPOSE 8080

ENTRYPOINT ["/sbin/tini", "--", "a2"]
CMD []
