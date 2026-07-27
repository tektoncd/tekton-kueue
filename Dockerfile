# Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:9.7-1775042950@sha256:4b7a967ce9c283ecfb1e7a186f7b97d3f35a479bcc38d3c03a0707f3c36e974d AS builder
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

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY internal/ internal/
COPY pkg/ pkg/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go


FROM registry.access.redhat.com/ubi9-micro@sha256:b1e86b97028b8fcfb6d85f997c39e6b6b67496163ef8d80d243220a4918e8bef
WORKDIR /
COPY --from=builder /opt/app-root/src/manager .
COPY LICENSE /licenses/
USER 65532:65532

# It is mandatory to set these labels
LABEL \
    com.redhat.component="openshift-kueue-rhel9-container" \
    cpe="cpe:/a:redhat:openshift_pipelines:0.3::el9" \
    description="Red Hat OpenShift Pipelines tekton-kueue kueue" \
    io.k8s.description="Red Hat OpenShift Pipelines tekton-kueue kueue" \
    io.k8s.display-name="Red Hat OpenShift Pipelines tekton-kueue kueue" \
    io.openshift.tags="tekton,openshift,tekton-kueue,kueue" \
    maintainer="pipelines-extcomm@redhat.com" \
    name="openshift-pipelines/kueue-rhel9" \
    summary="Red Hat OpenShift Pipelines tekton-kueue kueue" \
    version="v0.3.1"

ENTRYPOINT ["/manager"]
