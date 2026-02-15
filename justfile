# Binary name
binary := "aa"

# Run the agent with go run (faster for development)
run:
    go run ./cmd/aagent

# Run backend API server only
server:
    go run ./cmd/aagent server

# Install air hot-reload tool
install-air:
    go install github.com/air-verse/air@latest

# Run backend API server with hot reload (build must succeed before restart)
dev:
    air -c .air.toml

# Build the binary
build:
    go build -o {{binary}} ./cmd/aagent

# Run the built binary
start: build
    ./{{binary}}

# Clean build artifacts
clean:
    rm -f {{binary}}
    go clean

# Run tests
test:
    go test -v ./...

# Install to GOPATH/bin
install:
    go install ./cmd/aagent

# Format code
fmt:
    go fmt ./...

# Lint code
lint:
    go vet ./...

# View logs
logs:
    go run ./cmd/aagent logs

# Follow logs in real-time
logs-follow:
    go run ./cmd/aagent logs -f

# Build for all platforms
build-all:
    GOOS=linux GOARCH=amd64 go build -o {{binary}}-linux-amd64 ./cmd/aagent
    GOOS=linux GOARCH=arm64 go build -o {{binary}}-linux-arm64 ./cmd/aagent
    GOOS=darwin GOARCH=amd64 go build -o {{binary}}-darwin-amd64 ./cmd/aagent
    GOOS=darwin GOARCH=arm64 go build -o {{binary}}-darwin-arm64 ./cmd/aagent
    GOOS=windows GOARCH=amd64 go build -o {{binary}}-windows-amd64.exe ./cmd/aagent
