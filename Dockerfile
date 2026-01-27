# Stage 1: Build
FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.25-openshift-4.21 AS builder

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the controller binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod -a \
    -ldflags="-w -s" \
    -o vsphere-migration-controller \
    ./cmd/vsphere-migration-controller

# Stage 2: Runtime
FROM registry.ci.openshift.org/ocp/4.22:base-rhel9

# Copy the binary from builder
COPY --from=builder /workspace/vsphere-migration-controller /usr/bin/vsphere-migration-controller

# Set permissions
RUN chmod +x /usr/bin/vsphere-migration-controller

# Run as non-root user
USER 1001

ENTRYPOINT ["/usr/bin/vsphere-migration-controller"]
