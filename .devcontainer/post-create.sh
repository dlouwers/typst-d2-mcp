#!/bin/bash
set -e

echo "Setting up development environment..."

# Install Go dependencies
go mod download

echo "✅ Development environment ready!"
