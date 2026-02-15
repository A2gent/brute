# Binary name
binary := "aa"

# Run the agent with go run (faster for development)
run: moss-docker-up
    go run ./cmd/aagent

# Run backend API server only
server: moss-docker-up
    go run ./cmd/aagent server

# Install air hot-reload tool
install-air:
    go install github.com/air-verse/air@latest

# Run backend API server with hot reload (build must succeed before restart)
dev: moss-docker-up
    air -c .air.toml

# Build the binary
build:
    go build -o {{binary}} ./cmd/aagent

# Run the built binary
start: build moss-docker-up
    ./{{binary}}

# Build Docker image for MOSS realtime service
moss-docker-build:
    docker build -t aagent-moss-tts:latest ./docker/moss_tts_realtime

# Ensure MOSS realtime service container is running
moss-docker-up:
    @set -eu; \
    if ! command -v docker >/dev/null 2>&1; then \
      echo "error: docker is required for moss_tts_realtime" >&2; \
      exit 1; \
    fi; \
    if [ -z "$(docker images -q aagent-moss-tts:latest)" ]; then \
      just moss-docker-build; \
    fi; \
    if [ -n "$$(docker ps --filter 'name=^/aagent-moss-tts$$' --quiet)" ]; then \
      echo "moss docker service already running"; \
    elif [ -n "$$(docker ps -a --filter 'name=^/aagent-moss-tts$$' --quiet)" ]; then \
      docker start aagent-moss-tts >/dev/null; \
      echo "moss docker service started"; \
    else \
      docker run -d --name aagent-moss-tts -p 8099:8099 \
        -e MOSS_DEVICE=cpu \
        -v "$HOME/.cache/huggingface:/root/.cache/huggingface" \
        aagent-moss-tts:latest >/dev/null; \
      echo "moss docker service created and started"; \
    fi

# Stop and remove MOSS realtime service container
moss-docker-down:
    @set -eu; \
    if command -v docker >/dev/null 2>&1; then \
      docker rm -f aagent-moss-tts >/dev/null 2>&1 || true; \
    fi

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
