package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const localDockerAgentHealthProbeTimeout = 800 * time.Millisecond

var localDockerPortReservations = struct {
	sync.Mutex
	ports map[int]struct{}
}{ports: map[int]struct{}{}}

func reserveLocalDockerPort(port int) (func(), bool) {
	if port <= 0 {
		return func() {}, true
	}
	localDockerPortReservations.Lock()
	if _, reserved := localDockerPortReservations.ports[port]; reserved {
		localDockerPortReservations.Unlock()
		return func() {}, false
	}
	localDockerPortReservations.ports[port] = struct{}{}
	localDockerPortReservations.Unlock()
	return func() {
		localDockerPortReservations.Lock()
		delete(localDockerPortReservations.ports, port)
		localDockerPortReservations.Unlock()
	}, true
}

var dockerContainerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type dockerPSRow struct {
	ID         string `json:"ID"`
	Image      string `json:"Image"`
	Command    string `json:"Command"`
	CreatedAt  string `json:"CreatedAt"`
	RunningFor string `json:"RunningFor"`
	Ports      string `json:"Ports"`
	Status     string `json:"Status"`
	State      string `json:"State"`
	Names      string `json:"Names"`
	Labels     string `json:"Labels"`
}

func resolveLocalAgentDockerBuildPaths() (string, string, error) {
	if rawPath := strings.TrimSpace(os.Getenv("A2GENT_LOCAL_AGENT_DOCKERFILE")); rawPath != "" {
		dockerfilePath := rawPath
		if !filepath.IsAbs(dockerfilePath) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", "", fmt.Errorf("failed to resolve current directory: %w", err)
			}
			dockerfilePath = filepath.Join(cwd, dockerfilePath)
		}
		dockerfilePath = filepath.Clean(dockerfilePath)
		if !fileExists(dockerfilePath) {
			return "", "", fmt.Errorf("dockerfile not found at %q", dockerfilePath)
		}

		contextDir := strings.TrimSpace(os.Getenv("A2GENT_LOCAL_AGENT_DOCKER_CONTEXT"))
		if contextDir == "" {
			contextDir = filepath.Dir(dockerfilePath)
		}
		if !filepath.IsAbs(contextDir) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", "", fmt.Errorf("failed to resolve current directory: %w", err)
			}
			contextDir = filepath.Join(cwd, contextDir)
		}
		contextDir = filepath.Clean(contextDir)
		if !dirExists(contextDir) {
			return "", "", fmt.Errorf("docker build context directory not found at %q", contextDir)
		}
		return dockerfilePath, contextDir, nil
	}

	candidates := make([]string, 0, 4)
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "brute", "Dockerfile"))
		candidates = append(candidates, filepath.Join(cwd, "Dockerfile"))
	}
	if executable, err := os.Executable(); err == nil {
		execDir := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(execDir, "..", "brute", "Dockerfile"))
		candidates = append(candidates, filepath.Join(execDir, "Dockerfile"))
	}

	for _, candidate := range candidates {
		cleaned := filepath.Clean(candidate)
		if fileExists(cleaned) {
			return cleaned, filepath.Dir(cleaned), nil
		}
	}

	return "", "", fmt.Errorf(
		"unable to find Dockerfile (checked default locations). Set A2GENT_LOCAL_AGENT_DOCKERFILE and optional A2GENT_LOCAL_AGENT_DOCKER_CONTEXT",
	)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func boolPtr(v bool) *bool {
	return &v
}

var runCommand = defaultRunCommand

func defaultRunCommand(ctx context.Context, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, trimmed)
	}
	return trimmed, nil
}

func parseDockerLabels(raw string) map[string]string {
	labels := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		if key != "" {
			labels[key] = value
		}
	}
	return labels
}

func sanitizeDockerLabelValue(value string) string {
	if value == "" {
		return ""
	}
	normalized := strings.NewReplacer(",", "_", "\n", " ", "\r", " ").Replace(value)
	return strings.TrimSpace(normalized)
}

func parseHostPort(ports string) int {
	if ports == "" {
		return 0
	}
	chunks := strings.Split(ports, ",")
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		idx := strings.Index(chunk, "->8080/tcp")
		if idx == -1 {
			continue
		}
		prefix := chunk[:idx]
		colon := strings.LastIndex(prefix, ":")
		if colon == -1 || colon+1 >= len(prefix) {
			continue
		}
		portStr := strings.TrimSpace(prefix[colon+1:])
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
			return port
		}
	}
	return 0
}

func parsePublishedHostPorts(ports string) []int {
	if ports == "" {
		return nil
	}
	seen := map[int]struct{}{}
	for _, chunk := range strings.Split(ports, ",") {
		chunk = strings.TrimSpace(chunk)
		arrow := strings.Index(chunk, "->")
		if arrow == -1 {
			continue
		}
		hostSide := chunk[:arrow]
		colon := strings.LastIndex(hostSide, ":")
		if colon == -1 || colon+1 >= len(hostSide) {
			continue
		}
		rawPort := strings.TrimSpace(hostSide[colon+1:])
		if dash := strings.Index(rawPort, "-"); dash >= 0 {
			start, startErr := strconv.Atoi(strings.TrimSpace(rawPort[:dash]))
			end, endErr := strconv.Atoi(strings.TrimSpace(rawPort[dash+1:]))
			if startErr != nil || endErr != nil || start <= 0 || end < start {
				continue
			}
			for port := start; port <= end; port++ {
				seen[port] = struct{}{}
			}
			continue
		}
		if port, err := strconv.Atoi(rawPort); err == nil && port > 0 {
			seen[port] = struct{}{}
		}
	}
	portsOut := make([]int, 0, len(seen))
	for port := range seen {
		portsOut = append(portsOut, port)
	}
	return portsOut
}

func isBruteContainer(row dockerPSRow, labels map[string]string) bool {
	if strings.EqualFold(labels[localAgentManagerLabelKey], localAgentManagerLabelValue) {
		return true
	}
	img := strings.ToLower(strings.TrimSpace(row.Image))
	name := strings.ToLower(strings.TrimSpace(row.Names))
	if strings.Contains(img, "a2gent-brute") || strings.Contains(img, "/brute") {
		return true
	}
	if strings.Contains(name, "a2gent-brute") || strings.Contains(name, "brute") {
		return true
	}
	if service := strings.ToLower(strings.TrimSpace(labels["com.docker.compose.service"])); service == "brute" || service == "brute-tui" {
		return true
	}
	return false
}

func listLocalBruteContainers(ctx context.Context) ([]LocalDockerAgent, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := runCommand(cmdCtx, "docker", "ps", "-a", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("failed to list docker containers: %w", err)
	}
	if strings.TrimSpace(output) == "" {
		return []LocalDockerAgent{}, nil
	}
	lines := strings.Split(output, "\n")
	agents := make([]LocalDockerAgent, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row dockerPSRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		labels := parseDockerLabels(row.Labels)
		if !isBruteContainer(row, labels) {
			continue
		}
		hostPort := parseHostPort(row.Ports)
		running := strings.EqualFold(row.State, "running")
		agent := LocalDockerAgent{
			ID:        row.ID,
			Name:      row.Names,
			Image:     row.Image,
			State:     row.State,
			Status:    row.Status,
			CreatedAt: row.CreatedAt,
			Ports:     row.Ports,
			Labels:    labels,
			Managed:   strings.EqualFold(labels[localAgentManagerLabelKey], localAgentManagerLabelValue),
			Running:   running,
			HostPort:  hostPort,
		}
		if hostPort > 0 {
			agent.APIURL = fmt.Sprintf("http://127.0.0.1:%d", hostPort)
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

type localDockerAgentHealthPayload struct {
	Status        string                 `json:"status"`
	Reason        string                 `json:"reason"`
	Message       string                 `json:"message"`
	ProviderUsage *ProviderUsageResponse `json:"provider_usage"`
}

func annotateLocalDockerAgentHealth(ctx context.Context, agents []LocalDockerAgent) {
	if len(agents) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client := &http.Client{Timeout: localDockerAgentHealthProbeTimeout}
	var wg sync.WaitGroup
	for i := range agents {
		if !agents[i].Running || strings.TrimSpace(agents[i].APIURL) == "" {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			health := probeLocalDockerAgentHealth(ctx, client, agents[idx].APIURL)
			agents[idx].Health = &health
		}(i)
	}
	wg.Wait()
}

func probeLocalDockerAgentHealth(ctx context.Context, client *http.Client, baseURL string) LocalDockerAgentHealth {
	health := LocalDockerAgentHealth{
		Status:    "unavailable",
		Healthy:   false,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if client == nil {
		client = &http.Client{Timeout: localDockerAgentHealthProbeTimeout}
	}
	probeCtx, cancel := context.WithTimeout(ctx, localDockerAgentHealthProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		health.Message = err.Error()
		return health
	}
	resp, err := client.Do(req)
	if err != nil {
		health.Message = err.Error()
		return health
	}
	defer resp.Body.Close()

	health.HTTPStatus = resp.StatusCode
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var payload localDockerAgentHealthPayload
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	payloadStatus := strings.TrimSpace(payload.Status)
	if payloadStatus != "" {
		health.Status = payloadStatus
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		health.Status = "ok"
	} else {
		health.Status = "unhealthy"
	}
	health.Reason = strings.TrimSpace(payload.Reason)
	health.Message = strings.TrimSpace(payload.Message)
	health.ProviderUsage = payload.ProviderUsage
	if health.Message == "" && len(body) > 0 && payloadStatus == "" {
		health.Message = strings.TrimSpace(string(body))
	}
	health.Healthy = resp.StatusCode >= 200 && resp.StatusCode < 300 && (payloadStatus == "" || payloadStatus == "ok" || payloadStatus == "healthy")
	return health
}

func localDockerAgentAvailableForUse(agent LocalDockerAgent) bool {
	if !agent.Running {
		return false
	}
	if agent.Health == nil {
		return true
	}
	return agent.Health.Healthy
}

func findLocalBruteContainer(ctx context.Context, containerID string) (*LocalDockerAgent, error) {
	agents, err := listLocalBruteContainers(ctx)
	if err != nil {
		return nil, err
	}
	needle := strings.TrimSpace(containerID)
	for i := range agents {
		if agents[i].ID == needle || agents[i].Name == needle || strings.HasPrefix(agents[i].ID, needle) {
			return &agents[i], nil
		}
	}
	return nil, fmt.Errorf("container %q not found", containerID)
}

func dockerPublishedHostPorts(ctx context.Context) map[int]struct{} {
	used := map[int]struct{}{}
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := runCommand(cmdCtx, "docker", "ps", "-a", "--format", "{{json .}}")
	if err != nil {
		return used
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row dockerPSRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		for _, port := range parsePublishedHostPorts(row.Ports) {
			used[port] = struct{}{}
		}
	}
	return used
}

func localDockerReservedHostPorts() map[int]struct{} {
	localDockerPortReservations.Lock()
	defer localDockerPortReservations.Unlock()
	reserved := make(map[int]struct{}, len(localDockerPortReservations.ports))
	for port := range localDockerPortReservations.ports {
		reserved[port] = struct{}{}
	}
	return reserved
}

var hostPortListenable = func(port int) bool {
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
		if err != nil {
			return false
		}
		_ = ln.Close()
	}
	return true
}

func firstAvailableLocalDockerPort(ctx context.Context, start, end int, reserved map[int]struct{}) (int, error) {
	usedByDocker := dockerPublishedHostPorts(ctx)
	for port := start; port <= end; port++ {
		if _, used := usedByDocker[port]; used {
			continue
		}
		if _, used := reserved[port]; used {
			continue
		}
		if hostPortListenable(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available ports in range %d-%d", start, end)
}

func reserveAvailableLocalDockerPort(ctx context.Context, start, end int) (int, func(), error) {
	localDockerPortReservations.Lock()
	defer localDockerPortReservations.Unlock()
	port, err := firstAvailableLocalDockerPort(ctx, start, end, localDockerPortReservations.ports)
	if err != nil {
		return 0, func() {}, err
	}
	localDockerPortReservations.ports[port] = struct{}{}
	return port, func() {
		localDockerPortReservations.Lock()
		delete(localDockerPortReservations.ports, port)
		localDockerPortReservations.Unlock()
	}, nil
}

func findAvailablePort(ctx context.Context, start, end int) (int, error) {
	return firstAvailableLocalDockerPort(ctx, start, end, localDockerReservedHostPorts())
}
