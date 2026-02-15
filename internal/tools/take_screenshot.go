package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultScreenshotFormat     = "png"
	defaultScreenshotTarget     = "main"
	defaultScreenshotDirKey     = "AAGENT_SCREENSHOT_OUTPUT_DIR"
	defaultScreenshotDir        = "/tmp"
	defaultScreenshotDisplayKey = "AAGENT_SCREENSHOT_DISPLAY_INDEX"
)

type ScreenshotArea struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type TakeScreenshotParams struct {
	OutputPath   string          `json:"output_path,omitempty"`
	OutputDir    string          `json:"output_dir,omitempty"`
	Filename     string          `json:"filename,omitempty"`
	Format       string          `json:"format,omitempty"` // png | jpg | jpeg
	Target       string          `json:"target,omitempty"` // main | all | display | area
	DisplayIndex int             `json:"display_index,omitempty"`
	Area         *ScreenshotArea `json:"area,omitempty"`
}

type TakeScreenshotTool struct {
	workDir string
}

func NewTakeScreenshotTool(workDir string) *TakeScreenshotTool {
	return &TakeScreenshotTool{workDir: workDir}
}

func (t *TakeScreenshotTool) Name() string {
	return "take_screenshot_tool"
}

func (t *TakeScreenshotTool) Description() string {
	return `Capture a screenshot of the current display setup.
Supports: main display, all displays, a specific display index, or a rectangular area.
You can control where the screenshot file is stored via output_path/output_dir/filename.
If not provided, it uses the default screenshot settings configured in the Tools UI.`
}

func (t *TakeScreenshotTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"output_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional output file path, or directory path if no extension is provided. Relative paths are resolved from project workdir.",
			},
			"output_dir": map[string]interface{}{
				"type":        "string",
				"description": "Optional output directory. Ignored when output_path points to a file.",
			},
			"filename": map[string]interface{}{
				"type":        "string",
				"description": "Optional filename. If omitted, a timestamp-based name is used.",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "Image format: png or jpg (default: png).",
				"enum":        []string{"png", "jpg", "jpeg"},
			},
			"target": map[string]interface{}{
				"type":        "string",
				"description": "Capture target: main, all, display, or area (default: main).",
				"enum":        []string{"main", "all", "display", "area"},
			},
			"display_index": map[string]interface{}{
				"type":        "integer",
				"description": "1-based display index. Used when target=display (falls back to the configured default display when available).",
			},
			"area": map[string]interface{}{
				"type":        "object",
				"description": "Rectangle to capture. Required when target=area.",
				"properties": map[string]interface{}{
					"x": map[string]interface{}{
						"type":        "integer",
						"description": "Top-left X coordinate.",
					},
					"y": map[string]interface{}{
						"type":        "integer",
						"description": "Top-left Y coordinate.",
					},
					"width": map[string]interface{}{
						"type":        "integer",
						"description": "Capture width (> 0).",
					},
					"height": map[string]interface{}{
						"type":        "integer",
						"description": "Capture height (> 0).",
					},
				},
				"required": []string{"x", "y", "width", "height"},
			},
		},
	}
}

func (t *TakeScreenshotTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p TakeScreenshotParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	format, err := normalizeScreenshotFormat(p.Format, p.Filename, p.OutputPath)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	target := strings.ToLower(strings.TrimSpace(p.Target))
	defaultDisplayIndex := configuredDefaultDisplayIndex()
	if target == "" {
		if p.Area != nil {
			target = "area"
		} else if p.DisplayIndex > 0 || defaultDisplayIndex > 0 {
			target = "display"
		} else {
			target = defaultScreenshotTarget
		}
	}
	if target == "display" && p.DisplayIndex <= 0 {
		p.DisplayIndex = defaultDisplayIndex
	}

	if err := validateScreenshotTarget(target, p.DisplayIndex, p.Area); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	absPath, err := t.resolveOutputPath(p, format)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve output path: %w", err)
	}

	if err := captureScreenshot(ctx, target, p.DisplayIndex, p.Area, format, absPath); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	info, statErr := os.Stat(absPath)
	if statErr != nil {
		return nil, fmt.Errorf("screenshot command completed but output file is missing: %w", statErr)
	}

	payload := map[string]interface{}{
		"path":   absPath,
		"target": target,
		"format": format,
		"bytes":  info.Size(),
	}

	if rel, err := filepath.Rel(t.workDir, absPath); err == nil {
		payload["relative_path"] = rel
	}
	if target == "display" {
		payload["display_index"] = p.DisplayIndex
	}
	if target == "area" && p.Area != nil {
		payload["area"] = p.Area
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode result: %w", err)
	}
	return &Result{
		Success: true,
		Output:  string(out),
		Metadata: map[string]interface{}{
			"image_file": map[string]interface{}{
				"path":        absPath,
				"format":      format,
				"bytes":       info.Size(),
				"source_tool": t.Name(),
			},
		},
	}, nil
}

func normalizeScreenshotFormat(raw string, filename string, outputPath string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "" {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(filename)), "."))
		if ext == "" {
			ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(outputPath)), "."))
		}
		switch ext {
		case "jpg", "jpeg":
			format = "jpg"
		case "png":
			format = "png"
		default:
			format = defaultScreenshotFormat
		}
	}

	switch format {
	case "jpeg":
		format = "jpg"
	case "png", "jpg":
	default:
		return "", fmt.Errorf("unsupported format %q (expected png or jpg)", format)
	}
	return format, nil
}

func configuredDefaultDisplayIndex() int {
	raw := strings.TrimSpace(os.Getenv(defaultScreenshotDisplayKey))
	if raw == "" {
		return 0
	}
	idx, err := strconv.Atoi(raw)
	if err != nil || idx <= 0 {
		return 0
	}
	return idx
}

func validateScreenshotTarget(target string, displayIndex int, area *ScreenshotArea) error {
	switch target {
	case "main", "all":
		if displayIndex > 0 {
			return fmt.Errorf("display_index is only valid when target=display")
		}
		if area != nil {
			return fmt.Errorf("area is only valid when target=area")
		}
	case "display":
		if displayIndex <= 0 {
			return fmt.Errorf("display_index must be > 0 when target=display")
		}
		if area != nil {
			return fmt.Errorf("area is only valid when target=area")
		}
	case "area":
		if area == nil {
			return fmt.Errorf("area is required when target=area")
		}
		if area.Width <= 0 || area.Height <= 0 {
			return fmt.Errorf("area.width and area.height must be > 0")
		}
		if displayIndex > 0 {
			return fmt.Errorf("display_index cannot be combined with target=area")
		}
	default:
		return fmt.Errorf("unsupported target %q (expected main, all, display, or area)", target)
	}
	return nil
}

func (t *TakeScreenshotTool) resolveOutputPath(p TakeScreenshotParams, format string) (string, error) {
	resolvePath := func(raw string) string {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return ""
		}
		if filepath.IsAbs(raw) {
			return raw
		}
		return filepath.Join(t.workDir, raw)
	}

	filename := strings.TrimSpace(p.Filename)
	if filename == "" {
		filename = fmt.Sprintf("screenshot-%s.%s", time.Now().Format("20060102-150405"), format)
	} else if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), "."); ext == "" {
		filename += "." + format
	}

	outputPath := resolvePath(p.OutputPath)
	if outputPath != "" {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(outputPath)), ".")
		if ext != "" {
			if ext == "jpeg" {
				ext = "jpg"
			}
			if ext != format {
				return "", fmt.Errorf("output_path extension .%s does not match format %q", ext, format)
			}
			if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
				return "", err
			}
			return outputPath, nil
		}
		if err := os.MkdirAll(outputPath, 0755); err != nil {
			return "", err
		}
		return filepath.Join(outputPath, filename), nil
	}

	outputDir := resolvePath(p.OutputDir)
	if outputDir == "" {
		envDir := strings.TrimSpace(os.Getenv(defaultScreenshotDirKey))
		if envDir != "" {
			outputDir = resolvePath(envDir)
		} else {
			outputDir = defaultScreenshotDir
		}
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(outputDir, filename), nil
}

func captureScreenshot(ctx context.Context, target string, displayIndex int, area *ScreenshotArea, format string, outputPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return captureScreenshotDarwin(ctx, target, displayIndex, area, format, outputPath)
	case "linux":
		return captureScreenshotLinux(ctx, target, displayIndex, area, outputPath)
	case "windows":
		return captureScreenshotWindows(ctx, target, displayIndex, area, format, outputPath)
	default:
		return fmt.Errorf("screenshot capture is not supported on %s", runtime.GOOS)
	}
}

func captureScreenshotDarwin(ctx context.Context, target string, displayIndex int, area *ScreenshotArea, format string, outputPath string) error {
	args := []string{"-x", "-t", format}
	switch target {
	case "main":
		args = append(args, "-m")
	case "all":
		// Default screencapture behavior.
	case "display":
		args = append(args, "-D", strconv.Itoa(displayIndex))
	case "area":
		args = append(args, "-R", fmt.Sprintf("%d,%d,%d,%d", area.X, area.Y, area.Width, area.Height))
	default:
		return fmt.Errorf("unsupported target %q for darwin", target)
	}
	args = append(args, outputPath)
	return runCommand(ctx, "screencapture", args...)
}

func captureScreenshotLinux(ctx context.Context, target string, displayIndex int, area *ScreenshotArea, outputPath string) error {
	if target == "display" {
		return fmt.Errorf("target=display is not currently supported on linux")
	}

	if _, err := exec.LookPath("grim"); err == nil {
		args := []string{}
		if target == "area" && area != nil {
			args = append(args, "-g", fmt.Sprintf("%d,%d %dx%d", area.X, area.Y, area.Width, area.Height))
		}
		args = append(args, outputPath)
		return runCommand(ctx, "grim", args...)
	}

	if _, err := exec.LookPath("scrot"); err == nil {
		args := []string{"-z"}
		if target == "area" && area != nil {
			args = append(args, "-a", fmt.Sprintf("%d,%d,%d,%d", area.X, area.Y, area.Width, area.Height))
		}
		args = append(args, outputPath)
		return runCommand(ctx, "scrot", args...)
	}

	if _, err := exec.LookPath("import"); err == nil {
		args := []string{"-window", "root"}
		if target == "area" && area != nil {
			args = append(args, "-crop", fmt.Sprintf("%dx%d+%d+%d", area.Width, area.Height, area.X, area.Y))
		}
		args = append(args, outputPath)
		return runCommand(ctx, "import", args...)
	}

	return fmt.Errorf("no supported screenshot binary found (tried grim, scrot, import)")
}

func captureScreenshotWindows(ctx context.Context, target string, displayIndex int, area *ScreenshotArea, format string, outputPath string) error {
	shell := "powershell"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "pwsh"
		if _, err := exec.LookPath(shell); err != nil {
			return fmt.Errorf("neither powershell nor pwsh is available")
		}
	}

	formatName := "Png"
	if format == "jpg" {
		formatName = "Jpeg"
	}

	args := []string{
		outputPath,
		target,
		strconv.Itoa(displayIndex),
		formatName,
		"0",
		"0",
		"0",
		"0",
	}
	if area != nil {
		args[4] = strconv.Itoa(area.X)
		args[5] = strconv.Itoa(area.Y)
		args[6] = strconv.Itoa(area.Width)
		args[7] = strconv.Itoa(area.Height)
	}

	script := `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$path = $args[0]
$target = $args[1]
$displayIndex = [int]$args[2]
$formatName = $args[3]
$x = [int]$args[4]
$y = [int]$args[5]
$w = [int]$args[6]
$h = [int]$args[7]

if ($target -eq "area") {
	if ($w -le 0 -or $h -le 0) { throw "area width and height must be > 0" }
	$bounds = New-Object System.Drawing.Rectangle($x, $y, $w, $h)
} elseif ($target -eq "all") {
	$bounds = [System.Windows.Forms.SystemInformation]::VirtualScreen
} elseif ($target -eq "display") {
	$screens = [System.Windows.Forms.Screen]::AllScreens
	if ($displayIndex -lt 1 -or $displayIndex -gt $screens.Length) {
		throw ("display_index out of range: " + $displayIndex)
	}
	$bounds = $screens[$displayIndex - 1].Bounds
} else {
	$bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
}

$bmp = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height)
$gfx = [System.Drawing.Graphics]::FromImage($bmp)
$gfx.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
$imgFormat = [System.Drawing.Imaging.ImageFormat]::$formatName
$bmp.Save($path, $imgFormat)
$gfx.Dispose()
$bmp.Dispose()
`

	cmdArgs := []string{"-NoProfile", "-NonInteractive", "-Command", script}
	cmdArgs = append(cmdArgs, args...)
	return runCommand(ctx, shell, cmdArgs...)
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s failed: %s", name, msg)
	}
	return nil
}

var _ Tool = (*TakeScreenshotTool)(nil)
