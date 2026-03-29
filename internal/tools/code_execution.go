package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	defaultCodeExecutionTimeout = 10 * time.Second
	maxCodeExecutionTimeout     = 60 * time.Second
	maxCodeExecutionSize        = 64 * 1024
	maxCodeExecutionInputSize   = 256 * 1024
)

var pythonVersionPattern = regexp.MustCompile(`Python\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)

type CodeExecutionTool struct {
	workDir     string
	pythonExec  string
	pythonVer   string
	available   bool
	lookupError string
}

type CodeExecutionParams struct {
	Code    string      `json:"code"`
	Input   interface{} `json:"input,omitempty"`
	Timeout int         `json:"timeout,omitempty"` // milliseconds
}

type codeExecutionEnvelope struct {
	Code  string      `json:"code"`
	Input interface{} `json:"input"`
}

type codeExecutionResponse struct {
	OK            bool            `json:"ok"`
	Output        json.RawMessage `json:"output"`
	Logs          []string        `json:"logs"`
	Error         string          `json:"error"`
	PythonPath    string          `json:"python_executable"`
	PythonVersion string          `json:"python_version"`
}

func NewCodeExecutionTool(workDir string) *CodeExecutionTool {
	pythonExec, pythonVer, lookupErr := resolvePythonRuntime()
	tool := &CodeExecutionTool{
		workDir:    workDir,
		pythonExec: pythonExec,
		pythonVer:  pythonVer,
		available:  lookupErr == nil,
	}
	if lookupErr != nil {
		tool.lookupError = lookupErr.Error()
	}
	return tool
}

func (t *CodeExecutionTool) Name() string {
	return "code_execution"
}

func (t *CodeExecutionTool) Description() string {
	if !t.available {
		return "Execute secure Python snippets for structured input->output data processing. Python runtime is currently unavailable on this machine."
	}
	return fmt.Sprintf("Execute secure Python snippets for structured input->output data processing using %s (%s).", t.pythonExec, t.pythonVer)
}

func (t *CodeExecutionTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type":        "string",
				"description": "Python code to execute. `input_data` is provided. Return data via `output_data = ...`, or define `main(input_data)` and return a value.",
			},
			"input": map[string]interface{}{
				"description": "Any JSON-serializable input value passed to `input_data`.",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Execution timeout in milliseconds (default: 10000, max: 60000).",
			},
		},
		"required": []string{"code"},
	}
}

func (t *CodeExecutionTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p CodeExecutionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if !t.available {
		msg := "python runtime is not available"
		if t.lookupError != "" {
			msg = msg + ": " + t.lookupError
		}
		return &Result{Success: false, Error: msg}, nil
	}

	p.Code = strings.TrimSpace(p.Code)
	if p.Code == "" {
		return &Result{Success: false, Error: "code is required"}, nil
	}
	if len(p.Code) > maxCodeExecutionSize {
		return &Result{Success: false, Error: fmt.Sprintf("code is too large (%d > %d bytes)", len(p.Code), maxCodeExecutionSize)}, nil
	}

	timeout := defaultCodeExecutionTimeout
	if p.Timeout > 0 {
		timeout = time.Duration(p.Timeout) * time.Millisecond
	}
	if timeout > maxCodeExecutionTimeout {
		timeout = maxCodeExecutionTimeout
	}

	envelope := codeExecutionEnvelope{Code: p.Code, Input: p.Input}
	stdinPayload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize input: %w", err)
	}
	if len(stdinPayload) > maxCodeExecutionInputSize {
		return &Result{Success: false, Error: fmt.Sprintf("input payload is too large (%d > %d bytes)", len(stdinPayload), maxCodeExecutionInputSize)}, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, t.pythonExec, "-I", "-B", "-S", "-c", codeExecutionRunnerScript)
	cmd.Dir = t.workDir
	cmd.Stdin = bytes.NewReader(stdinPayload)
	cmd.Env = []string{"PYTHONIOENCODING=utf-8"}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	outStr := truncateOutput(stdout.String())
	errStr := truncateOutput(stderr.String())

	if runErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("python code timed out after %v", timeout),
				Output:  outStr,
				Metadata: map[string]interface{}{
					"python_executable": t.pythonExec,
					"python_version":    t.pythonVer,
				},
			}, nil
		}
		combined := strings.TrimSpace(strings.Join([]string{outStr, errStr}, "\n"))
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("python execution failed: %v", runErr),
			Output:  combined,
			Metadata: map[string]interface{}{
				"python_executable": t.pythonExec,
				"python_version":    t.pythonVer,
			},
		}, nil
	}

	var resp codeExecutionResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		combined := strings.TrimSpace(strings.Join([]string{outStr, errStr}, "\n"))
		if combined == "" {
			combined = outStr
		}
		return &Result{
			Success: false,
			Error:   "failed to decode python execution response",
			Output:  combined,
			Metadata: map[string]interface{}{
				"python_executable": t.pythonExec,
				"python_version":    t.pythonVer,
			},
		}, nil
	}

	if !resp.OK {
		errMsg := strings.TrimSpace(resp.Error)
		if errMsg == "" {
			errMsg = "python execution failed"
		}
		return &Result{
			Success: false,
			Error:   errMsg,
			Metadata: map[string]interface{}{
				"python_executable": firstNonEmpty(resp.PythonPath, t.pythonExec),
				"python_version":    firstNonEmpty(resp.PythonVersion, t.pythonVer),
			},
		}, nil
	}

	output := "null"
	if len(resp.Output) > 0 {
		if pretty, ok := prettyJSON(resp.Output); ok {
			output = pretty
		} else {
			output = string(resp.Output)
		}
	}

	if len(resp.Logs) > 0 {
		output = output + "\n\nlogs:\n" + strings.Join(resp.Logs, "\n")
	}

	return &Result{
		Success: true,
		Output:  output,
		Metadata: map[string]interface{}{
			"python_executable": firstNonEmpty(resp.PythonPath, t.pythonExec),
			"python_version":    firstNonEmpty(resp.PythonVersion, t.pythonVer),
		},
	}, nil
}

func resolvePythonRuntime() (string, string, error) {
	candidates := []string{
		"python3.13",
		"python3.12",
		"python3.11",
		"python3.10",
		"python3.9",
		"python3.8",
		"python3",
	}

	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		version, err := detectPythonVersion(path)
		if err != nil {
			continue
		}
		return path, version, nil
	}
	return "", "", fmt.Errorf("no supported python3 interpreter found in PATH")
}

func detectPythonVersion(pythonExec string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonExec, "--version")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", err
	}

	match := pythonVersionPattern.FindStringSubmatch(strings.TrimSpace(output.String()))
	if len(match) < 2 {
		return strings.TrimSpace(output.String()), nil
	}
	return "Python " + match[1], nil
}

func prettyJSON(raw json.RawMessage) (string, bool) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", false
	}
	return string(pretty), true
}

func truncateOutput(s string) string {
	if len(s) <= maxOutputSize {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:maxOutputSize]) + "\n... (output truncated)"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

const codeExecutionRunnerScript = `
import ast
import builtins
import json
import os
import sys
import traceback

ALLOWED_MODULES = {
    "array",
    "bisect",
    "collections",
    "collections.abc",
    "csv",
    "datetime",
    "decimal",
    "fractions",
    "functools",
    "heapq",
    "itertools",
    "json",
    "math",
    "operator",
    "random",
    "re",
    "statistics",
    "string",
    "textwrap",
}

BLOCKED_MODULE_PREFIXES = (
    "asyncio",
    "builtins",
    "ctypes",
    "http",
    "importlib",
    "inspect",
    "io",
    "marshal",
    "multiprocessing",
    "os",
    "pathlib",
    "pickle",
    "pkgutil",
    "runpy",
    "shutil",
    "site",
    "socket",
    "subprocess",
    "sys",
    "tempfile",
    "threading",
    "urllib",
    "venv",
    "zipfile",
)

BLOCKED_BUILTINS = {
    "__import__",
    "breakpoint",
    "compile",
    "eval",
    "exec",
    "globals",
    "help",
    "input",
    "locals",
    "setattr",
    "getattr",
    "delattr",
    "vars",
}

BLOCKED_CALLS = {
    ("os", "system"),
    ("os", "popen"),
    ("subprocess", "run"),
    ("subprocess", "Popen"),
    ("subprocess", "call"),
}

SAFE_BUILTINS = {
    "abs": abs,
    "all": all,
    "any": any,
    "bool": bool,
    "dict": dict,
    "enumerate": enumerate,
    "Exception": Exception,
    "filter": filter,
    "float": float,
    "int": int,
    "len": len,
    "list": list,
    "map": map,
    "max": max,
    "min": min,
    "pow": pow,
    "range": range,
    "repr": repr,
    "reversed": reversed,
    "round": round,
    "set": set,
    "sorted": sorted,
    "str": str,
    "sum": sum,
    "tuple": tuple,
    "zip": zip,
}

LOGS = []

def safe_print(*args, **kwargs):
    text = " ".join(str(arg) for arg in args)
    LOGS.append(text)

SAFE_BUILTINS["print"] = safe_print

def safe_import(name, globals=None, locals=None, fromlist=(), level=0):
    if level and level != 0:
        raise RuntimeError("relative imports are not allowed")
    root = name.split(".", 1)[0]
    if name in ALLOWED_MODULES or root in ALLOWED_MODULES:
        return __import__(name, globals, locals, fromlist, level)
    raise RuntimeError(f"import '{name}' is not allowed")

SAFE_BUILTINS["__import__"] = safe_import

def safe_open(file, mode="r", buffering=-1, encoding=None, errors=None, newline=None, closefd=True, opener=None):
    if opener is not None:
        raise RuntimeError("custom file openers are not allowed")
    if not isinstance(file, (str, bytes)):
        raise RuntimeError("open path must be a string")

    cwd = os.path.realpath(os.getcwd())
    target = os.path.realpath(os.path.join(cwd, file) if not os.path.isabs(file) else file)
    try:
        if os.path.commonpath([cwd, target]) != cwd:
            raise RuntimeError("open path is outside the workspace")
    except ValueError:
        raise RuntimeError("open path is outside the workspace")

    return builtins.open(target, mode, buffering, encoding, errors, newline, closefd)

SAFE_BUILTINS["open"] = safe_open

def emit(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False))

def blocked_module(name):
    if name in ALLOWED_MODULES:
        return False
    root = name.split(".", 1)[0]
    if root in ALLOWED_MODULES:
        return False
    for prefix in BLOCKED_MODULE_PREFIXES:
        if name == prefix or name.startswith(prefix + "."):
            return True
    return True

def inspect_ast(tree):
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                if blocked_module(alias.name):
                    raise RuntimeError(f"import '{alias.name}' is not allowed")
        elif isinstance(node, ast.ImportFrom):
            module = node.module or ""
            if blocked_module(module):
                raise RuntimeError(f"import from '{module}' is not allowed")
        elif isinstance(node, ast.Call):
            fn = node.func
            if isinstance(fn, ast.Name):
                if fn.id in BLOCKED_BUILTINS:
                    raise RuntimeError(f"call to '{fn.id}' is not allowed")
            elif isinstance(fn, ast.Attribute) and isinstance(fn.value, ast.Name):
                if (fn.value.id, fn.attr) in BLOCKED_CALLS:
                    raise RuntimeError(f"call to '{fn.value.id}.{fn.attr}' is not allowed")

def main():
    try:
        payload = json.loads(sys.stdin.read() or "{}")
        code = payload.get("code", "")
        input_data = payload.get("input")
        tree = ast.parse(code, mode="exec")
        inspect_ast(tree)

        safe_globals = {"__builtins__": SAFE_BUILTINS}
        local_vars = {"input_data": input_data}
        compiled = compile(tree, "<code_execution>", "exec")
        exec(compiled, safe_globals, local_vars)

        if "output_data" in local_vars:
            output = local_vars["output_data"]
        elif "main" in local_vars and callable(local_vars["main"]):
            output = local_vars["main"](input_data)
        else:
            output = None

        json.dumps(output, ensure_ascii=False)
        emit({
            "ok": True,
            "output": output,
            "logs": LOGS,
            "python_executable": sys.executable,
            "python_version": sys.version.split()[0],
        })
    except Exception as exc:
        emit({
            "ok": False,
            "error": f"{type(exc).__name__}: {exc}",
            "logs": LOGS,
            "traceback": traceback.format_exc(limit=5),
            "python_executable": sys.executable,
            "python_version": sys.version.split()[0],
        })

if __name__ == "__main__":
    main()
`

var _ Tool = (*CodeExecutionTool)(nil)
