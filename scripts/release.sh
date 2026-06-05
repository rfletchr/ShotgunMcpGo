#!/usr/bin/env bash
set -euo pipefail

TAG="${1:?usage: release.sh <tag>}"

go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...

go generate ./...

CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o "sg-mcp-linux-amd64" .
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o "sg-mcp-linux-arm64" .
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -o "sg-mcp-darwin-amd64" .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o "sg-mcp-darwin-arm64" .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "sg-mcp-windows-amd64.exe" .

gh release create "$TAG" \
    --title "$TAG" \
    --notes "$(git tag -l --format='%(contents)' "$TAG")" \
    sg-mcp-linux-amd64 \
    sg-mcp-linux-arm64 \
    sg-mcp-darwin-amd64 \
    sg-mcp-darwin-arm64 \
    sg-mcp-windows-amd64.exe
