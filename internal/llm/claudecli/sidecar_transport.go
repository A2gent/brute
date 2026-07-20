package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

func (c *Client) chatViaSidecar(
	ctx context.Context,
	request *llm.ChatRequest,
	model string,
	onEvent func(llm.StreamEvent) error,
) (*llm.ChatResponse, error) {
	sidecarPath, err := resolveSidecarPath(c.options)
	if err != nil {
		return nil, err
	}
	nodePath, err := resolveNodePath(c.options)
	if err != nil {
		return nil, err
	}
	claudePath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Claude CLI executable %q was not found in PATH; install Claude Code or set AAGENT_CLAUDE_CLI_PATH", c.options.Executable)
	}

	runRequest, err := buildSidecarRunRequest(request, model, c.options, claudePath)
	if err != nil {
		return nil, err
	}
	runLine, err := encodeNDJSONLine(runRequest)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, nodePath, sidecarPath)
	cmd.Dir = c.options.WorkDir
	cmd.Env = optionsCommandEnv(c.options)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open sidecar stdin: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open sidecar stdout: %w", err)
	}
	var stderr limitedBuffer
	stderr.limit = defaultMaxOutputBytes
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Claude Agent SDK sidecar: %w", err)
	}
	started := true
	unsafeAfterStart := func(err error) error {
		if err == nil || !started {
			return err
		}
		return llm.UnsafeForRetry(err)
	}

	var stdinMu sync.Mutex
	writeStdin := func(value interface{}) error {
		line, err := encodeNDJSONLine(value)
		if err != nil {
			return err
		}
		stdinMu.Lock()
		defer stdinMu.Unlock()
		_, err = stdinPipe.Write(line)
		return err
	}

	if _, err := stdinPipe.Write(runLine); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("failed to write run_request: %w", err)
	}

	sessionID := strings.TrimSpace(request.SessionID)
	approvalTimeout := resolveApprovalTimeout(c.options)
	takeResponse := c.options.TakeApprovalResponse

	processor := newStreamProcessor(onEvent)
	var stdout limitedBuffer
	stdout.limit = defaultMaxOutputBytes

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 64*1024), defaultMaxOutputBytes)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = stdout.Write([]byte(line))
		_, _ = stdout.Write([]byte("\n"))
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		msg, err := parseSidecarTypedMessage(trimmed)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, unsafeAfterStart(fmt.Errorf("failed to parse sidecar stdout line: %w", err))
		}

		switch sidecarMessageType(msg) {
		case sidecarMsgPermissionRequest:
			permReq, err := parseSidecarPermissionRequest(trimmed)
			if err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return nil, unsafeAfterStart(err)
			}
			params := approvalParamsFromPermissionRequest(sessionID, permReq)
			params.Timeout = approvalTimeout
			result, decisionErr := c.options.Broker.Request(ctx, params)
			var resolvePayload ApprovalResolvePayload
			if result.RequestID != "" && takeResponse != nil {
				if payload, ok := takeResponse(result.RequestID); ok {
					resolvePayload = payload
				}
			}
			resp := permissionResponseFromDecision(permReq, result, decisionErr, resolvePayload)
			if err := writeStdin(resp); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				return nil, unsafeAfterStart(fmt.Errorf("failed to write permission_response: %w", err))
			}
			continue
		case "error":
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			message, _ := msg["message"].(string)
			if strings.TrimSpace(message) == "" {
				message = "Claude Agent SDK sidecar returned an error"
			}
			return nil, unsafeAfterStart(fmt.Errorf("%s", normalizeClaudeCLIErrorMessage(message)))
		case "warning":
			warning, _ := msg["message"].(string)
			if warning == "" {
				warning = "sidecar warning"
			}
			if err := processor.emit(llm.StreamEvent{
				Type:           llm.StreamEventRuntimeWarning,
				RuntimeStatus:  "sidecar_warning",
				RuntimeWarning: warning,
			}); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return nil, unsafeAfterStart(err)
			}
			continue
		}

		event, err := parseCLIStreamEnvelope(trimmed)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, unsafeAfterStart(err)
		}
		if err := processor.handleEnvelope(event); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, unsafeAfterStart(err)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, unsafeAfterStart(fmt.Errorf("failed to read sidecar stdout: %w", scanErr))
	}

	_ = stdinPipe.Close()
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, unsafeAfterStart(fmt.Errorf("Claude Agent SDK sidecar failed: %s", normalizeClaudeCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String()))))
	}

	finalContent := strings.TrimSpace(processor.finalResult.Result)
	if finalContent == "" {
		finalContent = strings.TrimSpace(processor.content.String())
	}
	if finalContent == "" {
		finalContent = strings.TrimSpace(processor.assistantContent)
	}
	if finalContent == "" {
		finalContent = strings.TrimSpace(processor.finalResult.Message)
	}
	if finalContent == "" && !processor.sawResult {
		finalContent = strings.TrimSpace(stdout.String())
	}
	if processor.finalResult.IsError || strings.Contains(strings.ToLower(processor.finalResult.Subtype), "error") {
		msg := firstNonEmpty(finalContent, processor.finalResult.Error, "Claude Agent SDK sidecar returned an error result")
		return nil, unsafeAfterStart(fmt.Errorf("%s", normalizeClaudeCLIErrorMessage(msg)))
	}

	if err := processor.finalize(onEvent); err != nil {
		return nil, unsafeAfterStart(err)
	}
	logging.LogResponseWithContent(processor.usage.InputTokens, processor.usage.OutputTokens, 0, finalContent, nil)
	return &llm.ChatResponse{
		Content:               finalContent,
		Usage:                 processor.usage,
		StopReason:            firstNonEmpty(processor.stopReason, processor.finalResult.StopReason, processor.finalResult.Subtype),
		ProviderSessionCursor: c.providerSessionCursor(firstNonEmpty(processor.providerSessionCursor, processor.finalResult.SessionID)),
		ToolCalls:             nil,
	}, nil
}

// decodeRunRequestOptions is used by tests to inspect the first run_request line.
func decodeRunRequestOptions(line string) (map[string]interface{}, error) {
	var req sidecarRunRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return nil, err
	}
	if req.Type != sidecarMsgRunRequest {
		return nil, fmt.Errorf("expected %s", sidecarMsgRunRequest)
	}
	return req.Options, nil
}
