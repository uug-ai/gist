# syntax=docker/dockerfile:1.7
# Hardened image: static binary on scratch, runs as 65532, read-only root.
# Built by uug-ai/workflows release-create (hardened, profile standalone).
# Gists are written to GIST_DATA_DIR (default /data), a mounted volume.

FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS builder

ENV GOTOOLCHAIN=local

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY *.go ./
COPY internal/ ./internal/
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    test "$(go env GOVERSION)" = go1.26.11 && \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
    -mod=readonly -trimpath -buildvcs=false \
    -tags timetzdata,netgo \
    -ldflags "-w -buildid= -X main.version=${VERSION}" \
    -o /out/main .
RUN install -d -m 0755 /out/runtime /out/runtime/etc /out/runtime/etc/ssl /out/runtime/etc/ssl/certs && \
    install -m 0444 /etc/ssl/certs/ca-certificates.crt /out/runtime/etc/ssl/certs/ca-certificates.crt

FROM builder AS test
RUN --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    go vet -tags=timetzdata,netgo ./... && \
    go test -p=1 -mod=readonly -tags=timetzdata,netgo ./... && \
    CGO_ENABLED=1 go test -p=1 -race -mod=readonly -tags=timetzdata,netgo ./... && \
    go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags=timetzdata,netgo ./... && \
    go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -mode=binary /out/main

FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED
LABEL org.opencontainers.image.title="gist" \
      org.opencontainers.image.description="Share code and text snippets in one click" \
      org.opencontainers.image.source="https://github.com/uug-ai/gist" \
      org.opencontainers.image.vendor="UUG.AI" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
COPY --from=builder --chown=0:0 /out/runtime/ /
COPY --from=builder --chown=0:0 --chmod=0555 /out/main /app/main

USER 65532:65532
WORKDIR /app

# Listens on $PORT (default 8080).
ENTRYPOINT ["/app/main"]
