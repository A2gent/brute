package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/llm"
)

func testApprovalLimits() approval.Limits {
	return approval.Limits{
		MaxPending:     8,
		MaxInputBytes:  65536,
		DefaultTimeout: 300 * time.Millisecond,
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
}

func writeFakeNodeRunner(t *testing.T, dir string) string {
	t.Helper()
	node := filepath.Join(dir, "node")
	writeExecutable(t, node, "#!/bin/sh\nexec \"$1\"\n")
	return node
}

func sidecarClient(t *testing.T, broker *approval.Broker, sidecarPath, nodePath, workDir string, take func(string) (ApprovalResolvePayload, bool)) *Client {
	t.Helper()
	t.Setenv(envSidecarPath, sidecarPath)
	fakeClaude := filepath.Join(workDir, "claude")
	writeExecutable(t, fakeClaude, "#!/bin/sh\necho ok\n")
	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              workDir,
		NoSessionPersistence: true,
		Broker:               broker,
		SidecarPath:          sidecarPath,
		NodePath:             nodePath,
		ApprovalTimeout:      testApprovalLimits().DefaultTimeout,
		TakeApprovalResponse: take,
	})
	return client
}

func TestSidecarEnabledRequiresEnvAndBroker(t *testing.T) {
	broker := approval.New(testApprovalLimits())

	old := os.Getenv(envSidecarPath)
	t.Cleanup(func() { t.Setenv(envSidecarPath, old) })

	t.Setenv(envSidecarPath, "")
	if SidecarEnabled(Options{Broker: broker}) {
		t.Fatal("expected disabled without env")
	}
	if SidecarEnabled(Options{}) {
		t.Fatal("expected disabled without broker")
	}
	t.Setenv(envSidecarPath, "/tmp/sidecar.mjs")
	if !SidecarEnabled(Options{Broker: broker}) {
		t.Fatal("expected enabled with broker and env")
	}
}

func TestBuildSidecarRunRequestOmitsAutoApproveKnobs(t *testing.T) {
	req, err := buildSidecarRunRequest(&llm.ChatRequest{
		Messages:     []llm.Message{{Role: "user", Content: "hi"}},
		SystemPrompt: "extra",
		Tools:        []llm.ToolDefinition{{Name: "read"}},
	}, "claude-sonnet-4-6", Options{WorkDir: "/work", NoSessionPersistence: true}, "/bin/claude")
	if err != nil {
		t.Fatalf("buildSidecarRunRequest: %v", err)
	}
	if req.Type != sidecarMsgRunRequest || req.Prompt != "hi" {
		t.Fatalf("unexpected run request: %+v", req)
	}
	for _, key := range []string{sidecarStrippedOptionAllowed, sidecarStrippedOptionAccept, sidecarStrippedOptionPermMode} {
		if _, ok := req.Options[key]; ok {
			t.Fatalf("option %q must not be sent to sidecar", key)
		}
	}
	tools, ok := req.Options["tools"].([]string)
	if !ok {
		t.Fatalf("tools = %#v", req.Options["tools"])
	}
	if !containsString(tools, "AskUserQuestion") || !containsString(tools, "Read") {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestAskUserAnswersFromMessageUsesQuestionsArray(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Color?","header":"Color","options":[{"label":"Blue"}]}]}`)
	answers := askUserAnswersFromMessage(input, "Blue")
	if answers["Color?"] != "Blue" {
		t.Fatalf("answers = %#v", answers)
	}
	updated := buildAskUserUpdatedInput(permissionInputMap(input), answers)
	rawQuestions, ok := updated["questions"]
	if !ok || rawQuestions == nil {
		t.Fatalf("expected questions preserved: %#v", updated)
	}
}

func TestSidecarTransportAllowOnce(t *testing.T) {
	tmp := t.TempDir()
	runCapture := filepath.Join(tmp, "run.json")
	respCapture := filepath.Join(tmp, "resp.json")
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r RUN_LINE
printf '%s' "$RUN_LINE" > "$RUN_CAPTURE"
printf '%s\n' '{"type":"permission_request","requestId":"sdk-1","toolUseID":"tu-1","toolName":"Bash","input":{"command":"echo hi"}}'
read -r RESP_LINE
printf '%s' "$RESP_LINE" > "$RESP_CAPTURE"
printf '%s\n' '{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":1,"output_tokens":2}}'
`)
	t.Setenv("RUN_CAPTURE", runCapture)
	t.Setenv("RESP_CAPTURE", respCapture)

	broker := approval.New(testApprovalLimits())
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForBrokerPending(t, broker, 1)
		req := broker.Pending()[0]
		if err := broker.Resolve(req.ID, "sess-1", approval.DecisionAllowOnce); err != nil {
			t.Errorf("Resolve: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Chat(ctx, &llm.ChatRequest{
		SessionID: "sess-1",
		Messages:  []llm.Message{{Role: "user", Content: "hello"}},
		Tools:     []llm.ToolDefinition{{Name: "bash"}},
	})
	<-done
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("expected no tool calls, got %#v", resp.ToolCalls)
	}

	runRaw, err := os.ReadFile(runCapture)
	if err != nil {
		t.Fatalf("read run capture: %v", err)
	}
	opts, err := decodeRunRequestOptions(string(runRaw))
	if err != nil {
		t.Fatalf("decode run request: %v", err)
	}
	if _, ok := opts["allowedTools"]; ok {
		t.Fatal("run_request must not include allowedTools")
	}

	respRaw, err := os.ReadFile(respCapture)
	if err != nil {
		t.Fatalf("read resp capture: %v", err)
	}
	var permResp sidecarPermissionResponse
	if err := json.Unmarshal(respRaw, &permResp); err != nil {
		t.Fatalf("unmarshal permission response: %v", err)
	}
	if permResp.Behavior != "allow" {
		t.Fatalf("behavior = %q", permResp.Behavior)
	}
	if permResp.UpdatedInput["command"] != "echo hi" {
		t.Fatalf("updatedInput = %#v", permResp.UpdatedInput)
	}
}

func TestSidecarTransportAllowSessionCache(t *testing.T) {
	tmp := t.TempDir()
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r RUN_LINE
for ROUND in 1 2; do
  printf '%s\n' "{\"type\":\"permission_request\",\"requestId\":\"sdk-$ROUND\",\"toolUseID\":\"tu-$ROUND\",\"toolName\":\"Bash\",\"input\":{\"command\":\"echo $ROUND\"}}"
  read -r RESP_LINE
done
printf '%s\n' '{"type":"result","subtype":"success","result":"cached","usage":{"input_tokens":1,"output_tokens":1}}'
`)
	broker := approval.New(testApprovalLimits())
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitForBrokerPending(t, broker, 1)
		req := broker.Pending()[0]
		if err := broker.Resolve(req.ID, "sess-cache", approval.DecisionAllowSession); err != nil {
			t.Errorf("Resolve: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if len(broker.Pending()) != 0 {
			t.Errorf("expected session cache to skip second pending, got %d", len(broker.Pending()))
		}
	}()

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		SessionID: "sess-cache",
		Messages:  []llm.Message{{Role: "user", Content: "go"}},
	})
	wg.Wait()
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "cached" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestSidecarTransportDeny(t *testing.T) {
	tmp := t.TempDir()
	respCapture := filepath.Join(tmp, "resp.json")
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r _
printf '%s\n' '{"type":"permission_request","requestId":"sdk-deny","toolUseID":"tu-deny","toolName":"Write","input":{"file_path":"/tmp/x"}}'
read -r RESP_LINE
printf '%s' "$RESP_LINE" > "$RESP_CAPTURE"
printf '%s\n' '{"type":"result","subtype":"success","result":"denied-run"}'
`)
	t.Setenv("RESP_CAPTURE", respCapture)

	broker := approval.New(testApprovalLimits())
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, nil)
	go func() {
		waitForBrokerPending(t, broker, 1)
		req := broker.Pending()[0]
		_ = broker.Resolve(req.ID, "sess-deny", approval.DecisionDeny)
	}()

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		SessionID: "sess-deny",
		Messages:  []llm.Message{{Role: "user", Content: "write"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "denied-run" {
		t.Fatalf("content = %q", resp.Content)
	}
	raw, _ := os.ReadFile(respCapture)
	var permResp sidecarPermissionResponse
	_ = json.Unmarshal(raw, &permResp)
	if permResp.Behavior != "deny" {
		t.Fatalf("behavior = %q", permResp.Behavior)
	}
}

func TestSidecarTransportTimeout(t *testing.T) {
	tmp := t.TempDir()
	respCapture := filepath.Join(tmp, "resp.json")
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r _
printf '%s\n' '{"type":"permission_request","requestId":"sdk-timeout","toolUseID":"tu-timeout","toolName":"Bash","input":{"command":"sleep"}}'
read -r RESP_LINE
printf '%s' "$RESP_LINE" > "$RESP_CAPTURE"
printf '%s\n' '{"type":"result","subtype":"success","result":"after-timeout"}'
`)
	t.Setenv("RESP_CAPTURE", respCapture)

	broker := approval.New(testApprovalLimits())
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, nil)

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		SessionID: "sess-timeout",
		Messages:  []llm.Message{{Role: "user", Content: "slow"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "after-timeout" {
		t.Fatalf("content = %q", resp.Content)
	}
	raw, _ := os.ReadFile(respCapture)
	var permResp sidecarPermissionResponse
	_ = json.Unmarshal(raw, &permResp)
	if permResp.Behavior != "deny" || !strings.Contains(permResp.Message, "timed out") {
		t.Fatalf("permission response = %#v", permResp)
	}
}

func TestSidecarTransportCancellation(t *testing.T) {
	tmp := t.TempDir()
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r _
printf '%s\n' '{"type":"permission_request","requestId":"sdk-cancel","toolUseID":"tu-cancel","toolName":"Bash","input":{"command":"echo"}}'
read -r _
printf '%s\n' '{"type":"result","subtype":"success","result":"should-not"}'
`)
	broker := approval.New(testApprovalLimits())
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitForBrokerPending(t, broker, 1)
		cancel()
	}()

	_, err := client.Chat(ctx, &llm.ChatRequest{
		SessionID: "sess-cancel",
		Messages:  []llm.Message{{Role: "user", Content: "cancel"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSidecarTransportMalformedAndWarning(t *testing.T) {
	tmp := t.TempDir()
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r _
printf '%s\n' '{"type":"warning","message":"recoverable"}'
printf '%s\n' '{"type":"result","subtype":"success","result":"ok"}'
`)
	broker := approval.New(testApprovalLimits())
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, nil)

	var warnings []string
	resp, err := client.ChatStream(context.Background(), &llm.ChatRequest{
		SessionID: "sess-warn",
		Messages:  []llm.Message{{Role: "user", Content: "warn"}},
	}, func(ev llm.StreamEvent) error {
		if ev.Type == llm.StreamEventRuntimeWarning {
			warnings = append(warnings, ev.RuntimeWarning)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "ok" || len(warnings) == 0 {
		t.Fatalf("resp=%#v warnings=%#v", resp, warnings)
	}
}

func TestSidecarTransportOversizeLine(t *testing.T) {
	tmp := t.TempDir()
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	huge := strings.Repeat("a", defaultMaxOutputBytes+1024)
	writeExecutable(t, sidecar, "#!/bin/sh\nread -r _\nprintf '%s\\n' '"+huge+"'\n")
	broker := approval.New(testApprovalLimits())
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, nil)
	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		SessionID: "sess-big",
		Messages:  []llm.Message{{Role: "user", Content: "big"}},
	})
	if err == nil {
		t.Fatal("expected oversize line error")
	}
}

func TestSidecarTransportAskUserQuestionAnswers(t *testing.T) {
	tmp := t.TempDir()
	respCapture := filepath.Join(tmp, "resp.json")
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r _
printf '%s\n' '{"type":"permission_request","requestId":"sdk-ask","toolUseID":"tu-ask","toolName":"AskUserQuestion","input":{"questions":[{"question":"Pick?","options":[{"label":"A"},{"label":"B"}]}]}}'
read -r RESP_LINE
printf '%s' "$RESP_LINE" > "$RESP_CAPTURE"
printf '%s\n' '{"type":"result","subtype":"success","result":"answered"}'
`)
	t.Setenv("RESP_CAPTURE", respCapture)

	broker := approval.New(testApprovalLimits())
	resolvePayload := map[string]ApprovalResolvePayload{}
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, func(id string) (ApprovalResolvePayload, bool) {
		payload, ok := resolvePayload[id]
		return payload, ok
	})

	go func() {
		waitForBrokerPending(t, broker, 1)
		req := broker.Pending()[0]
		if req.AskUser == nil || req.AskUser.Question != "Pick?" {
			t.Errorf("unexpected ask user payload: %#v", req.AskUser)
		}
		resolvePayload[req.ID] = ApprovalResolvePayload{Message: "A"}
		_ = broker.Resolve(req.ID, "sess-ask", approval.DecisionAllowOnce)
	}()

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		SessionID: "sess-ask",
		Messages:  []llm.Message{{Role: "user", Content: "ask"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "answered" {
		t.Fatalf("content = %q", resp.Content)
	}
	raw, _ := os.ReadFile(respCapture)
	var permResp sidecarPermissionResponse
	_ = json.Unmarshal(raw, &permResp)
	if permResp.Answers["Pick?"] != "A" {
		t.Fatalf("answers = %#v", permResp.Answers)
	}
}

func TestSidecarTransportAskUserQuestionMultiAnswers(t *testing.T) {
	tmp := t.TempDir()
	respCapture := filepath.Join(tmp, "resp.json")
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
read -r _
printf '%s\n' '{"type":"permission_request","requestId":"sdk-ask-multi","toolUseID":"tu-ask-multi","toolName":"AskUserQuestion","input":{"questions":[{"question":"Color?","options":[{"label":"Red"}]},{"question":"Size?","options":[{"label":"Large"}]}]}}'
read -r RESP_LINE
printf '%s' "$RESP_LINE" > "$RESP_CAPTURE"
printf '%s\n' '{"type":"result","subtype":"success","result":"answered"}'
`)
	t.Setenv("RESP_CAPTURE", respCapture)

	broker := approval.New(testApprovalLimits())
	resolvePayload := map[string]ApprovalResolvePayload{}
	client := sidecarClient(t, broker, sidecar, writeFakeNodeRunner(t, tmp), tmp, func(id string) (ApprovalResolvePayload, bool) {
		payload, ok := resolvePayload[id]
		return payload, ok
	})

	wantAnswers := map[string]string{"Color?": "Red", "Size?": "Large"}
	go func() {
		waitForBrokerPending(t, broker, 1)
		req := broker.Pending()[0]
		resolvePayload[req.ID] = ApprovalResolvePayload{Answers: wantAnswers}
		_ = broker.Resolve(req.ID, "sess-ask-multi", approval.DecisionAllowOnce)
	}()

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		SessionID: "sess-ask-multi",
		Messages:  []llm.Message{{Role: "user", Content: "ask"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "answered" {
		t.Fatalf("content = %q", resp.Content)
	}
	raw, _ := os.ReadFile(respCapture)
	var permResp sidecarPermissionResponse
	_ = json.Unmarshal(raw, &permResp)
	if len(permResp.Answers) != len(wantAnswers) {
		t.Fatalf("answers = %#v, want %#v", permResp.Answers, wantAnswers)
	}
	for key, want := range wantAnswers {
		if permResp.Answers[key] != want {
			t.Fatalf("answers[%q] = %q, want %q", key, permResp.Answers[key], want)
		}
	}
}

func TestSidecarTransportUsesNodeWithoutShell(t *testing.T) {
	tmp := t.TempDir()
	runCapture := filepath.Join(tmp, "argv.txt")
	sidecar := filepath.Join(tmp, "sidecar.mjs")
	writeExecutable(t, sidecar, `#!/bin/sh
printf '%s\n' "$0" > "$ARGV0"
printf '%s\n' "$1" > "$ARGV1"
read -r _
printf '%s\n' '{"type":"result","subtype":"success","result":"argv-ok"}'
`)
	t.Setenv("ARGV0", runCapture+".0")
	t.Setenv("ARGV1", runCapture+".1")

	node := filepath.Join(tmp, "node")
	writeExecutable(t, node, "#!/bin/sh\nprintf '%s\\n' \"$0\" >> \""+runCapture+"\"\nprintf '%s\\n' \"$1\" >> \""+runCapture+"\"\nexec \""+sidecar+"\"\n")

	broker := approval.New(testApprovalLimits())
	t.Setenv(envSidecarPath, sidecar)
	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		WorkDir:              tmp,
		NoSessionPersistence: true,
		Broker:               broker,
		SidecarPath:          sidecar,
		NodePath:             node,
	})

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		SessionID: "sess-argv",
		Messages:  []llm.Message{{Role: "user", Content: "argv"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "argv-ok" {
		t.Fatalf("content = %q", resp.Content)
	}
	argv, err := os.ReadFile(runCapture)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	if len(lines) < 2 {
		t.Fatalf("argv capture = %q", string(argv))
	}
	if !strings.HasSuffix(lines[0], "/node") {
		t.Fatalf("expected node argv0, got %q", lines[0])
	}
	absSidecar, _ := filepath.Abs(sidecar)
	if lines[1] != absSidecar {
		t.Fatalf("expected absolute sidecar path %q, got %q", absSidecar, lines[1])
	}
	if strings.Contains(lines[0], "sh -c") || strings.Contains(lines[1], "sh -c") {
		t.Fatal("must not invoke shell wrapper")
	}
}

func TestDefaultCLIPathWhenSidecarDisabled(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeClaude := filepath.Join(tmp, "claude")
	writeExecutable(t, fakeClaude, "#!/bin/sh\n: > \"$ARGS_FILE\"\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"cli\"}'\n")
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv(envSidecarPath, "")

	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
		Broker:               approval.New(testApprovalLimits()),
	})
	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "cli" {
		t.Fatalf("content = %q", resp.Content)
	}
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("expected CLI invocation, args file missing: %v", err)
	}
}

func TestResolveSidecarPathRejectsDirectory(t *testing.T) {
	tmp := t.TempDir()
	_, err := resolveSidecarPath(Options{SidecarPath: tmp})
	if err == nil {
		t.Fatal("expected error for directory sidecar path")
	}
}

func TestResolveNodePathRequiresExecutable(t *testing.T) {
	tmp := t.TempDir()
	badNode := filepath.Join(tmp, "node")
	if err := os.WriteFile(badNode, []byte("not-exec"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveNodePath(Options{NodePath: badNode})
	if err == nil {
		t.Fatal("expected error for non-executable node")
	}
	if path, err := exec.LookPath("node"); err == nil {
		if resolved, err := resolveNodePath(Options{NodePath: path}); err != nil || resolved == "" {
			t.Fatalf("resolveNodePath(node) = %q, %v", resolved, err)
		}
	}
}

func waitForBrokerPending(t *testing.T, broker *approval.Broker, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(broker.Pending()) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d pending approvals, got %d", want, len(broker.Pending()))
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
