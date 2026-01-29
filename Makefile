.PHONY: all build test test-unit test-integration test-e2e clean lint fmt vet

# Build variables
BINDIR := bin
BINARY := vmware-cloud-foundation-migration
MAIN := cmd/vmware-cloud-foundation-migration/main.go

# Go parameters
GOCMD := go
GOBUILD := $(GOCMD) build
GOCLEAN := $(GOCMD) clean
GOTEST := $(GOCMD) test
GOGET := $(GOCMD) get
GOFMT := $(GOCMD) fmt
GOVET := $(GOCMD) vet

all: build

build:
	mkdir -p $(BINDIR)
	$(GOBUILD) -gcflags "all=-N -l" -o $(BINDIR)/$(BINARY) $(MAIN)

test: test-unit

test-unit:
	$(GOTEST) -v -race -coverprofile=coverage.txt -covermode=atomic ./pkg/...

test-integration:
	$(GOTEST) -v -race ./test/integration/...

test-e2e:
	@if [ "$(E2E_TEST)" != "true" ]; then \
		echo "E2E tests require E2E_TEST=true environment variable"; \
		exit 1; \
	fi
	$(GOTEST) -v -timeout 60m ./test/e2e/...

clean:
	$(GOCLEAN)
	rm -rf $(BINDIR)
	rm -f coverage.txt

lint:
	golangci-lint run --timeout 5m

fmt:
	$(GOFMT) ./...

vet:
	$(GOVET) ./...

# Code generation
generate:
	$(GOCMD) generate ./...

# Generate CRD manifests
manifests:
	controller-gen crd:crdVersions=v1 paths="./pkg/apis/..." output:crd:artifacts:config=deploy/crds

# Install tools
tools:
	$(GOGET) github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GOGET) sigs.k8s.io/controller-tools/cmd/controller-gen@latest
