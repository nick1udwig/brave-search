set shell := ["bash", "-euo", "pipefail", "-c"]

# Build the CLI binary at ./bravesearch.
build:
    go build -o bravesearch ./cmd/bravesearch

# Install to ~/.local/bin/bravesearch (delegates to existing installer script).
install:
    ./scripts/install.sh
