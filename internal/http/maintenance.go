package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	defaultRebuildBruteTimeout = 10 * time.Minute
	maxRebuildBruteTimeout     = 30 * time.Minute
)

type rebuildBruteRequest struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}
type rebuildBruteAndDockerImageRequest struct {
	TimeoutSeconds int    `json:"timeout_seconds"`
	Image          string `json:"image"`
	NoCache        bool   `json:"no_cache"`
}

type rebuildBruteAndDockerImageResponse struct {
	Status      string                           `json:"status"`
	Brute       systemCommandResponse            `json:"brute"`
	DockerImage localDockerAgentImageBuildResult `json:"docker_image"`
}

type systemCommandResponse struct {
	Status           string   `json:"status"`
	Command          string   `json:"command"`
	Args             []string `json:"args"`
	Output           string   `json:"output,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}

type restartSubAgentContainersRequest struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

type restartedSubAgentContainer struct {
	ContainerID       string `json:"container_id"`
	Name              string `json:"name"`
	AgentDefinitionID string `json:"agent_definition_id,omitempty"`
	Output            string `json:"output,omitempty"`
}

type skippedSubAgentContainer struct {
	ContainerID       string `json:"container_id"`
	Name              string `json:"name"`
	AgentDefinitionID string `json:"agent_definition_id,omitempty"`
	Reason            string `json:"reason"`
}

type failedSubAgentContainer struct {
	ContainerID       string `json:"container_id"`
	Name              string `json:"name"`
	AgentDefinitionID string `json:"agent_definition_id,omitempty"`
	Error             string `json:"error"`
}

type restartSubAgentContainersResponse struct {
	Status         string                       `json:"status"`
	RestartedCount int                          `json:"restarted_count"`
	SkippedCount   int                          `json:"skipped_count"`
	FailedCount    int                          `json:"failed_count"`
	Restarted      []restartedSubAgentContainer `json:"restarted"`
	Skipped        []skippedSubAgentContainer   `json:"skipped"`
	Failures       []failedSubAgentContainer    `json:"failures"`
}

func (s *Server) handleRebuildBrute(w http.ResponseWriter, r *http.Request) {
	var req rebuildBruteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	result, err := rebuildBrute(r.Context(), req.TimeoutSeconds)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to rebuild Brute:\n"+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleRebuildBruteAndDockerImage(w http.ResponseWriter, r *http.Request) {
	var req rebuildBruteAndDockerImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	bruteResult, err := rebuildBrute(r.Context(), req.TimeoutSeconds)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to rebuild Brute:\n"+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	dockerResult, err := buildLocalDockerAgentImage(ctx, buildLocalDockerAgentImageRequest{
		Image:   req.Image,
		NoCache: req.NoCache,
	})
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to rebuild Docker image:\n"+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, rebuildBruteAndDockerImageResponse{
		Status:      "rebuilt",
		Brute:       bruteResult,
		DockerImage: dockerResult,
	})
}

func rebuildBrute(ctx context.Context, timeoutSeconds int) (systemCommandResponse, error) {
	commandLine := strings.TrimSpace(os.Getenv("A2GENT_BRUTE_REBUILD_COMMAND"))
	if commandLine == "" {
		commandLine = defaultBruteRebuildCommandLine()
	}

	command, args, err := splitCommandLine(commandLine)
	if err != nil {
		return systemCommandResponse{}, fmt.Errorf("invalid rebuild command: %w", err)
	}

	timeout := boundedTimeoutSeconds(timeoutSeconds, defaultRebuildBruteTimeout, maxRebuildBruteTimeout)
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := runCommand(buildCtx, command, args...)
	if err != nil {
		return systemCommandResponse{}, err
	}
	cwd, _ := os.Getwd()
	return systemCommandResponse{
		Status:           "rebuilt",
		Command:          command,
		Args:             args,
		Output:           output,
		TimeoutSeconds:   int(timeout.Seconds()),
		WorkingDirectory: cwd,
	}, nil
}

func (s *Server) handleRestartRunningSubAgentContainers(w http.ResponseWriter, r *http.Request) {
	var req restartSubAgentContainersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	containers, err := listLocalBruteContainers(r.Context())
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list Docker containers: "+err.Error())
		return
	}

	perContainerTimeout := boundedTimeoutSeconds(req.TimeoutSeconds, 30*time.Second, 5*time.Minute)
	result := restartSubAgentContainersResponse{
		Status:    "completed",
		Restarted: []restartedSubAgentContainer{},
		Skipped:   []skippedSubAgentContainer{},
		Failures:  []failedSubAgentContainer{},
	}
	for _, container := range containers {
		defID := strings.TrimSpace(container.Labels[dockerRuntimeAgentDefLabelKey])
		base := skippedSubAgentContainer{
			ContainerID:       container.ID,
			Name:              container.Name,
			AgentDefinitionID: defID,
		}
		if defID == "" || !strings.EqualFold(container.Labels[dockerRuntimeManagedLabelKey], "true") {
			base.Reason = "not a managed sub-agent runtime container"
			result.Skipped = append(result.Skipped, base)
			continue
		}
		if !container.Running {
			base.Reason = "container is not running"
			result.Skipped = append(result.Skipped, base)
			continue
		}
		if !dockerContainerIDPattern.MatchString(container.ID) {
			result.Failures = append(result.Failures, failedSubAgentContainer{
				ContainerID:       container.ID,
				Name:              container.Name,
				AgentDefinitionID: defID,
				Error:             "invalid Docker container identifier",
			})
			continue
		}

		restartCtx, cancel := context.WithTimeout(r.Context(), perContainerTimeout)
		output, restartErr := runCommand(restartCtx, "docker", "restart", container.ID)
		cancel()
		if restartErr != nil {
			result.Failures = append(result.Failures, failedSubAgentContainer{
				ContainerID:       container.ID,
				Name:              container.Name,
				AgentDefinitionID: defID,
				Error:             restartErr.Error(),
			})
			continue
		}
		if s.dockerRuntime != nil {
			// Restarting containers keeps definitions warm; refresh idle tracking so the
			// reaper does not immediately stop a freshly restarted agent.
			s.dockerRuntime.touch(container.Name)
		}
		result.Restarted = append(result.Restarted, restartedSubAgentContainer{
			ContainerID:       container.ID,
			Name:              container.Name,
			AgentDefinitionID: defID,
			Output:            output,
		})
	}
	result.RestartedCount = len(result.Restarted)
	result.SkippedCount = len(result.Skipped)
	result.FailedCount = len(result.Failures)
	if result.FailedCount > 0 {
		result.Status = "partial_failure"
	}
	s.jsonResponse(w, http.StatusOK, result)
}

func defaultBruteRebuildCommandLine() string {
	output := "brute"
	if runtime.GOOS == "windows" {
		output = "brute.exe"
	}
	return "go build -o " + output + " ./cmd/aagent"
}

func boundedTimeoutSeconds(seconds int, fallback time.Duration, maximum time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	duration := time.Duration(seconds) * time.Second
	if duration > maximum {
		return maximum
	}
	return duration
}

func splitCommandLine(commandLine string) (string, []string, error) {
	parts, err := shellFields(commandLine)
	if err != nil {
		return "", nil, err
	}
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("command is empty")
	}
	return parts[0], parts[1:], nil
}

func shellFields(input string) ([]string, error) {
	var fields []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields, nil
}
