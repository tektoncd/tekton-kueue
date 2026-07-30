#!/bin/bash

# Extract a Go module version from go.mod by module path.
# Usage: ./hack/get-dep-version.sh <module-path>
# Example: ./hack/get-dep-version.sh sigs.k8s.io/kueue

set -e

if [ -z "$1" ]; then
    echo "Usage: $0 <module-path>" >&2
    echo "Example: $0 sigs.k8s.io/kueue" >&2
    exit 1
fi

MODULE="$1"

if [ ! -f "go.mod" ]; then
    echo "Error: go.mod file not found" >&2
    exit 1
fi

VERSION=$(grep "$MODULE" go.mod | awk '{print $2}')

if [ -z "$VERSION" ]; then
    echo "Error: Could not find $MODULE in go.mod" >&2
    exit 1
fi

echo "$VERSION"
