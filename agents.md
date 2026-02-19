# Agents Configuration

This document describes agent configurations and behaviors for the A²gent/brute system.

## TDD Workflow

This project supports two TDD approaches depending on the context and complexity.

### Approach 1: Parallel TDD (Recommended for AI-assisted development)

Write test and implementation code in parallel, then iterate:

1. **Design** - Understand requirements and design test cases + implementation together
2. **Write Both** - Create test file and implementation code in parallel
3. **Run Tests** - Execute `just test` and observe results
4. **Iterate** - Fix both test and code based on failures
5. **Refactor** - Improve code quality while keeping tests green
6. **Repeat** - Continue for the next feature

**When to use:**
- ✅ New features with clear requirements
- ✅ CRUD operations and utilities
- ✅ AI-assisted development (faster iteration)
- ✅ Straightforward business logic

**Benefits:**
- More efficient for AI code generation
- Faster for simple features
- Natural design process (test + code co-evolve)
- Less context switching

### Approach 2: Classic TDD (Test-first)

Strictly follow red-green-refactor cycle:

1. **Write Test First** - Create a failing test that describes the desired behavior
2. **Run Tests** - Execute `just test` and verify the test fails (RED)
3. **Implement Code** - Write minimal code to make the test pass
4. **Run Tests Again** - Execute `just test` and verify the test passes (GREEN)
5. **Refactor** - Improve code quality while keeping tests green
6. **Repeat** - Continue the cycle for the next feature

**When to use:**
- ✅ Complex business logic with unclear requirements
- ✅ Bug fixes (reproduce bug with failing test first)
- ✅ Refactoring existing code (tests as safety net)
- ✅ Exploring edge cases and error handling

**Benefits:**
- Forces clear thinking about requirements
- Ensures tests actually catch failures
- Safer for complex refactoring
- Better for exploratory development

### Practical Example: Parallel TDD

```bash
# 1. Write test and code in parallel (in separate files)
# - internal/utils/validator.go
# - internal/utils/validator_test.go

# 2. Run tests
just test

# 3. Iterate - fix failures in both test and code
# Edit files based on test output

# 4. Run again until green
just test

# 5. Refactor while keeping tests green
```

### Practical Example: Classic TDD

```bash
# 1. Write failing test first
# internal/utils/validator_test.go

# 2. Run and see it fail (RED)
just test

# 3. Write minimal implementation
# internal/utils/validator.go

# 4. Run and see it pass (GREEN)
just test

# 5. Refactor
```

### Running Tests

```bash
# Run all tests with verbose output
just test

# Run tests for specific package
cd brute && go test -v ./internal/agent

# Run tests with coverage
cd brute && go test -v -cover ./...

# Run specific test
cd brute && go test -v -run TestAgentName ./internal/agent

# Watch mode (with entr or similar)
find . -name "*.go" | entr -c just test
```

### Test Structure

Tests in this project follow Go conventions:

- Test files: `*_test.go`
- Test functions: `func TestXxx(t *testing.T)`
- Integration tests: `*_integration_test.go`

Example test structure:

```go
func TestFeatureName(t *testing.T) {
    // ARRANGE
    // Setup test data and dependencies
    
    // ACT
    // Execute the code under test
    
    // ASSERT
    // Verify expected outcomes
}
```

### Sub-tests

Break complex tests into focused sub-tests:

```go
func TestUserValidation(t *testing.T) {
    t.Run("valid email", func(t *testing.T) {
        // Test valid email case
    })
    
    t.Run("invalid email format", func(t *testing.T) {
        // Test invalid email case
    })
    
    t.Run("missing required fields", func(t *testing.T) {
        // Test missing fields case
    })
}
```

## Agent Types

### Terminal Agent (TUI)

The main terminal-based agent with interactive interface.

**Entry Point**: `cmd/aagent/main.go`

**Features**:
- Interactive TUI using Bubble Tea
- Session management
- Multi-provider LLM support
- Tool execution

### HTTP Server Agent

API server for web integration.

**Entry Point**: `cmd/aagent/main.go server`

**Features**:
- REST API endpoints
- WebSocket support
- Voice integration (Whisper)
- Session management API

## Agent Configuration

Agents are configured via environment variables and config files.

### Environment Variables

Key environment variables:

- `ANTHROPIC_API_KEY` - Claude API key
- `KIMI_API_KEY` - Kimi API key
- `GEMINI_API_KEY` - Gemini API key
- `AAGENT_DATA_PATH` - Data storage path
- `AAGENT_CONFIG_PATH` - Config file path

### Config File

Default location: `~/.config/aagent/config.yaml`

Example configuration:

```yaml
providers:
  - name: claude
    type: anthropic
    model: claude-3-5-sonnet-20241022
    enabled: true
  
  - name: gemini
    type: gemini
    model: gemini-2.0-flash-exp
    enabled: true

data_path: ~/.local/share/aagent/
log_path: ~/.local/share/aagent/logs/
```

## Development Workflow

### Adding New Features

**Choose your TDD approach:**

#### For Simple/Clear Features (Parallel TDD):
1. **Design** - Define requirements
2. **Write Both** - Test + implementation in parallel
3. **Run** - `just test` and iterate
4. **Validate** - Manual testing if needed
5. **Document** - Update relevant docs

#### For Complex/Unclear Features (Classic TDD):
1. **Design** - Define test cases first
2. **Test** - Write failing test (RED)
3. **Implement** - Write minimal code (GREEN)
4. **Refactor** - Improve while keeping tests green
5. **Validate** - Manual testing if needed
6. **Document** - Update relevant docs

### Decision Matrix

| Scenario | Recommended Approach |
|----------|---------------------|
| New CRUD endpoint | Parallel TDD |
| New utility function | Parallel TDD |
| Bug fix | Classic TDD (reproduce bug first) |
| Complex algorithm | Classic TDD (explore with tests) |
| Refactoring | Classic TDD (tests as safety net) |
| AI pair programming | Parallel TDD (faster iteration) |
| Unclear requirements | Classic TDD (tests clarify requirements) |

### Code Organization

```
brute/
├── cmd/aagent/           # Main entry point
├── internal/
│   ├── agent/           # Core agent logic
│   ├── config/          # Configuration handling
│   ├── http/            # HTTP server & handlers
│   ├── commands/        # CLI commands
│   └── ...
├── agents.md            # This file
└── README.md            # Project overview
```

## Testing Strategy

### Unit Tests

- Test individual functions and methods
- Mock external dependencies
- Fast execution
- Located alongside source code (`*_test.go`)

### Integration Tests

- Test component interactions
- Use real dependencies where feasible
- Slower execution
- Named with `*_integration_test.go` suffix

### Example Test Files

- `internal/agent/agent_test.go` - Agent unit tests
- `internal/http/a2a_handler_test.go` - HTTP handler tests
- `internal/http/speech_transcribe_integration_test.go` - Integration tests

## Contributing

When adding new agent features:

1. Follow TDD approach
2. Write clear, focused tests
3. Use descriptive test names
4. Add integration tests for cross-component features
5. Update this documentation
6. Follow code style guidelines in project

## Resources

- [Go Testing Package](https://pkg.go.dev/testing)
- [Table Driven Tests in Go](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)
- [Testing Best Practices](https://github.com/golang/go/wiki/TestComments)
