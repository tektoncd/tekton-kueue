# Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.25.8-1775042950@sha256:89736fc57b42eb91e0bfeb08f5eaafb296818a6e519cd64b7bfe155fe391a9dc AS builder
ARG TARGETOS
ARG TARGETARCH

ENV GOTOOLCHAIN=auto
WORKDIR /opt/app-root/src
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Coverage instrumentation build argument
ARG ENABLE_COVERAGE=false

# Copy the go source
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/

# Build with or without coverage instrumentation
RUN if [ "$ENABLE_COVERAGE" = "true" ]; then \
        echo "Building with coverage instrumentation..."; \
        CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -cover -covermode=atomic -tags=coverage -o manager ./cmd/; \
    else \
        echo "Building production binary..."; \
        CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager ./cmd/; \
    fi


FROM registry.access.redhat.com/ubi9-micro@sha256:b1e86b97028b8fcfb6d85f997c39e6b6b67496163ef8d80d243220a4918e8bef
WORKDIR /
COPY --from=builder /opt/app-root/src/manager .
COPY LICENSE /licenses/
USER 65532:65532

# It is mandatory to set these labels
LABEL name="Tekton Kueue Extension"
LABEL description="Tekton Kueue Extension"
LABEL com.redhat.component="Tekton Kueue Extension"
LABEL io.k8s.description="Tekton Kueue Extension"
LABEL io.k8s.display-name="Tekton Kueue Extension"

ENTRYPOINT ["/manager"]
