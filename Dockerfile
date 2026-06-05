FROM golang:1.26.4 AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
RUN go generate ./... && CGO_ENABLED=0 go build -o sg-mcp .

FROM gcr.io/distroless/static-debian12

COPY --from=builder /build/sg-mcp /sg-mcp

EXPOSE 3000
ENTRYPOINT ["/sg-mcp", "-http"]
