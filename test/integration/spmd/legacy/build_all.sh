#!/bin/bash

# Build and test all legacy compatibility examples

set -e

echo "Building and testing all legacy examples..."

for dir in variables functions types packages labels json_tags; do
    echo "Testing $dir..."
    cd "$dir"
    
    echo "  Building..."
    go build
    
    echo "  Running..."
    go run .
    
    echo "  ✓ $dir passed"
    cd ..
    echo
done

echo "All legacy examples build and run successfully!"
echo "This proves backward compatibility with existing Go code using 'uniform' and 'varying' as identifiers."