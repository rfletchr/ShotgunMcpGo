FROM golang:1.26.6 AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# pandoc is a build-time tool only (docs HTML -> markdown in `go generate`); it is
# not copied into the final image.
RUN apt-get update && apt-get install -y --no-install-recommends pandoc \
    && rm -rf /var/lib/apt/lists/*
RUN go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
RUN go generate ./... && CGO_ENABLED=0 go build -o sg-mcp .

FROM gcr.io/distroless/static-debian12

COPY --from=builder /build/sg-mcp /sg-mcp

EXPOSE 3000
ENTRYPOINT ["/sg-mcp", "-http"]
