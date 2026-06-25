# Build the manager binary
FROM registry.ci.openshift.org/stolostron/builder:go1.26-linux AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Cache deps from the checked-in module graph.
# Running `go mod tidy` before source is copied can prune requirements
# and cause nondeterministic version resolution during build.
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY controllers/ controllers/
COPY webhook/ webhook/
# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=1 GOOS="${TARGETOS:-linux}" go build -a -mod=readonly -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
