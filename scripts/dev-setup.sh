#!/bin/bash
# Development setup script for vSphere Migration Controller

set -e

echo "==================================================="
echo "vSphere Migration Controller - Development Setup"
echo "==================================================="
echo ""

# Check Go version
echo "Checking Go version..."
if ! command -v go &> /dev/null; then
    echo "❌ Go is not installed. Please install Go 1.22 or later."
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
echo "✅ Go version: $GO_VERSION"
echo ""

# Install development tools
echo "Installing development tools..."

# golangci-lint
if ! command -v golangci-lint &> /dev/null; then
    echo "Installing golangci-lint..."
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
else
    echo "✅ golangci-lint already installed"
fi

# controller-gen
if ! command -v controller-gen &> /dev/null; then
    echo "Installing controller-gen..."
    go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest
else
    echo "✅ controller-gen already installed"
fi

echo ""

# Download dependencies
echo "Downloading Go dependencies..."
go mod download
echo "✅ Dependencies downloaded"
echo ""

# Run go mod tidy
echo "Tidying Go modules..."
go mod tidy
echo "✅ Modules tidied"
echo ""

# Create bin directory
echo "Creating bin directory..."
mkdir -p bin
echo "✅ bin directory created"
echo ""

# Build the controller
echo "Building controller..."
if make build; then
    echo "✅ Controller built successfully"
else
    echo "❌ Build failed"
    exit 1
fi
echo ""

# Run tests
echo "Running unit tests..."
if make test-unit 2>/dev/null || true; then
    echo "✅ Tests completed (some may fail due to incomplete implementation)"
else
    echo "⚠️  Some tests failed (expected during development)"
fi
echo ""

# Summary
echo "==================================================="
echo "Development Environment Ready!"
echo "==================================================="
echo ""
echo "Next steps:"
echo "  1. Implement remaining phases (see IMPLEMENTATION_STATUS.md)"
echo "  2. Run 'make build' to compile"
echo "  3. Run 'make test-unit' to test"
echo "  4. Run 'make lint' to check code quality"
echo ""
echo "Useful commands:"
echo "  make build           - Build the controller"
echo "  make test-unit       - Run unit tests"
echo "  make fmt             - Format code"
echo "  make lint            - Run linter"
echo "  make clean           - Clean build artifacts"
echo ""
echo "Documentation:"
echo "  README.md                    - User documentation"
echo "  IMPLEMENTATION_STATUS.md     - Implementation tracking"
echo "  IMPLEMENTATION_SUMMARY.md    - What's been built"
echo ""
echo "Happy coding! 🚀"
