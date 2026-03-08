FROM mcr.microsoft.com/devcontainers/go:1.25

# Install D2
RUN curl -fsSL https://d2lang.com/install.sh | sh -s --

# Install Typst
RUN curl -fsSL https://github.com/typst/typst/releases/download/v0.14.2/typst-x86_64-unknown-linux-musl.tar.xz \
    | tar -xJ -C /usr/local/bin --strip-components=1

# Install linting tools
RUN go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest && \
    go install golang.org/x/vuln/cmd/govulncheck@latest && \
    go install github.com/fzipp/gocyclo/cmd/gocyclo@latest && \
    go install github.com/gordonklaus/ineffassign@latest && \
    go install github.com/client9/misspell/cmd/misspell@latest

# Fix Go module cache permissions for vscode user
RUN mkdir -p /go/pkg/mod /go/pkg/sumdb && \
    chown -R vscode:vscode /go
