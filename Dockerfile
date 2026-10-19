# Build the mode-operator binary.
# Base images are pinned by digest, and Dependabot updates the tag and digest
# together. hack/build-go-version.sh reads the Go version from this line, so
# CI and make vulncheck use the Go that builds the image. The builder runs on
# the build machine's platform and cross-compiles for the target, so building
# for another architecture needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.9@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS builder
ARG TARGETOS
ARG TARGETARCH
# VERSION is stamped into the binary and logged at startup.
ARG VERSION=dev

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o mode-operator cmd/main.go

# Use distroless as minimal base image to package the binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
# GHCR links an image to the repository named here, so the package inherits
# the repository's access permissions. Its visibility is set separately: a new
# package is private until made public in its settings.
LABEL org.opencontainers.image.source="https://github.com/bitasylum/mode-operator" \
      org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /
COPY --from=builder /workspace/mode-operator .
USER 65532:65532

ENTRYPOINT ["/mode-operator"]
