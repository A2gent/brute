# Binary name
binary := "brute"

# Run the agent with go run (faster for development)
run:
    go run ./cmd/aagent

# Run backend API server only
server:
    go run ./cmd/aagent server

# Install the CLI to ~/.local/bin (override with AAGENT_INSTALL_DIR)
install:
    ./install.sh

# Install optional local development dependencies
install-dev-deps:
    brew install cmake ffmpeg pkg-config
    go install github.com/air-verse/air@latest

build:
    go build -o {{binary}} ./cmd/aagent

# Run backend API server with hot reload (build must succeed before restart)
dev:
    go install github.com/air-verse/air@latest
    air -c .air.toml

# Run the built binary
start: build
    ./{{binary}} --port 5445

# Clean build artifacts
clean:
    rm -f {{binary}}
    go clean

# Run tests
test:
    go test -v ./...

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

# Run Docker compose in API-only mode (default container workflow)
docker-api:
    docker compose up --build brute

# Run Docker compose API mode with explicit LM Studio endpoint
docker-api-lmstudio lmstudio_url:
    LM_STUDIO_BASE_URL={{lmstudio_url}} docker compose up --build brute

# Stop Docker compose services
docker-api-down:
    docker compose down --remove-orphans

# Stop all Docker services for this project (alias)
docker-stop:
    docker compose down --remove-orphans

# Run interactive TUI inside Docker (attaches your terminal)
docker-tui:
    docker compose down --remove-orphans
    docker compose run --rm --build --service-ports -it brute-tui

# Run TUI service command in non-interactive mode (debug/fallback)
docker-tui-no-tty:
    docker compose run --rm --build --service-ports brute-tui server

# Force rebuild Docker image and re-run compose service (use when runtime code changed)
docker-rerun:
    docker compose down --remove-orphans
    docker rm -f a2gent-brute 2>/dev/null || true
    docker image rm a2gent-brute:latest 2>/dev/null || true
    docker compose build --no-cache brute
    docker compose up --force-recreate brute

# Build image with Apple's native container runtime (apple/container CLI)
apple-build:
    container build --tag a2gent-brute:latest --file Dockerfile .

# Run API-only with Apple container runtime
apple-api:
    container system start
    container run --rm --name a2gent-brute --publish 8080:8080 --volume ./:/workspace --volume ${HOME}/.a2gent-data:/data --env HOME=/data --env AAGENT_DATA_PATH=/data --env LM_STUDIO_BASE_URL=${LM_STUDIO_BASE_URL:-http://host.docker.internal:1234/v1} a2gent-brute:latest server

# Run interactive TUI with Apple container runtime
apple-tui:
    container system start
    container run --rm --name a2gent-brute-tui --interactive --tty --publish 8080:8080 --volume ./:/workspace --volume ${HOME}/.a2gent-data:/data --env HOME=/data --env AAGENT_DATA_PATH=/data --env LM_STUDIO_BASE_URL=${LM_STUDIO_BASE_URL:-http://host.docker.internal:1234/v1} a2gent-brute:latest

# Stop running Apple containers for this project
apple-stop:
    container stop a2gent-brute 2>/dev/null || true
    container stop a2gent-brute-tui 2>/dev/null || true

# Stop Apple container system VM/runtime
apple-system-stop:
    container system stop

# Build for all platforms
build-all:
    GOOS=linux GOARCH=amd64 go build -o {{binary}}-linux-amd64 ./cmd/aagent
    GOOS=linux GOARCH=arm64 go build -o {{binary}}-linux-arm64 ./cmd/aagent
    GOOS=darwin GOARCH=amd64 go build -o {{binary}}-darwin-amd64 ./cmd/aagent
    GOOS=darwin GOARCH=arm64 go build -o {{binary}}-darwin-arm64 ./cmd/aagent
    GOOS=windows GOARCH=amd64 go build -o {{binary}}-windows-amd64.exe ./cmd/aagent
