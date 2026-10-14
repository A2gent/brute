package speechengine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Phases a long-running engine call reports so the UI can say what is taking so long.
const (
	PhasePreparing    = "preparing"
	PhaseDownloading  = "downloading"
	PhaseLoading      = "loading"
	PhaseTranscribing = "transcribing"
)

// Progress is the latest known state of an engine call. Engines that run a slow external CLI
// (WhisperKit downloads gigabytes and compiles for the Neural Engine on first use) fill it in.
type Progress struct {
	Engine          string  `json:"engine"`
	Phase           string  `json:"phase"`
	Detail          string  `json:"detail,omitempty"`
	Percent         float64 `json:"percent,omitempty"`
	DownloadedBytes int64   `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64   `json:"total_bytes,omitempty"`
	ModelPath       string  `json:"model_path,omitempty"`
	StartedAt       int64   `json:"started_at_ms"`
	UpdatedAt       int64   `json:"updated_at_ms"`
	Active          bool    `json:"active"`
}

var progressTracker = struct {
	mu   sync.Mutex
	byID map[string]*Progress
}{byID: map[string]*Progress{}}

func updateProgress(engine string, apply func(*Progress)) {
	progressTracker.mu.Lock()
	defer progressTracker.mu.Unlock()
	p := progressTracker.byID[engine]
	if p == nil {
		p = &Progress{Engine: engine, StartedAt: time.Now().UnixMilli()}
		progressTracker.byID[engine] = p
	}
	apply(p)
	p.UpdatedAt = time.Now().UnixMilli()
}

func startProgress(engine string) {
	progressTracker.mu.Lock()
	progressTracker.byID[engine] = &Progress{
		Engine:    engine,
		Phase:     PhasePreparing,
		StartedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
		Active:    true,
	}
	progressTracker.mu.Unlock()
}

func finishProgress(engine string) {
	updateProgress(engine, func(p *Progress) { p.Active = false })
}

// CurrentProgress returns a snapshot of the latest call for an engine, if any.
func CurrentProgress(engine string) (Progress, bool) {
	progressTracker.mu.Lock()
	defer progressTracker.mu.Unlock()
	p := progressTracker.byID[normalizeEngineID(engine)]
	if p == nil {
		return Progress{}, false
	}
	return *p, true
}

var percentPattern = regexp.MustCompile(`(\d{1,3})%`)

// applyWhisperKitLine maps one line of `whisperkit-cli --verbose` output to a phase.
func applyWhisperKitLine(engine, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	switch {
	case strings.Contains(line, "Initializing models"):
		updateProgress(engine, func(p *Progress) { p.Phase = PhaseLoading })
	case strings.Contains(line, "Model initialization complete"), strings.Contains(line, "Starting transcription"):
		updateProgress(engine, func(p *Progress) { p.Phase = PhaseTranscribing })
	}
	if strings.Contains(line, "Elapsed Time") {
		if m := percentPattern.FindStringSubmatch(line); m != nil {
			if pct, err := strconv.Atoi(m[1]); err == nil && pct >= 0 && pct <= 100 {
				updateProgress(engine, func(p *Progress) {
					p.Phase = PhaseTranscribing
					p.Percent = float64(pct)
				})
			}
		}
	}
}

// lineWriter splits a stream on \n and \r (progress bars redraw with \r) and passes each line on.
type lineWriter struct {
	buf    []byte
	onLine func(string)
	next   io.Writer
}

func (w *lineWriter) Write(data []byte) (int, error) {
	if w.next != nil {
		if _, err := w.next.Write(data); err != nil {
			return 0, err
		}
	}
	for _, b := range data {
		if b == '\n' || b == '\r' {
			if len(w.buf) > 0 {
				w.onLine(string(w.buf))
				w.buf = w.buf[:0]
			}
			continue
		}
		if len(w.buf) < 4096 {
			w.buf = append(w.buf, b)
		}
	}
	return len(data), nil
}

func whisperKitModelWatch(model string) modelWatch {
	repo, subdir := engineModelRepo(EngineWhisperKit, model)
	return modelWatch{root: engineModelDir(EngineWhisperKit, model), repo: repo, subdir: subdir}
}

func whisperKitModelRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Documents", "huggingface", "models", "argmaxinc", "whisperkit-coreml")
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// modelWatch describes where a model's files land while a CLI downloads them.
type modelWatch struct {
	root string
	// repo/subdir identify the Hugging Face source, used to learn the total download size.
	repo, subdir string
}

func hasIncompleteFile(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), ".incomplete") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// hfHubCacheDir mirrors huggingface_hub's cache resolution.
func hfHubCacheDir() string {
	if dir := strings.TrimSpace(os.Getenv("HF_HUB_CACHE")); dir != "" {
		return dir
	}
	if home := strings.TrimSpace(os.Getenv("HF_HOME")); home != "" {
		return filepath.Join(home, "hub")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userHome, ".cache", "huggingface", "hub")
}

// hfModelWatch watches a Hugging Face repo id ("org/name") in the hub cache. The weight index, which is
// small and downloads first, tells how large the full download will be.
func hfModelWatch(model string) modelWatch {
	cache := hfHubCacheDir()
	model = strings.TrimSpace(model)
	if cache == "" || !strings.Contains(model, "/") {
		return modelWatch{}
	}
	root := filepath.Join(cache, "models--"+strings.ReplaceAll(model, "/", "--"))
	return modelWatch{root: root, repo: model}
}

// watchModelDownload reports DownloadedBytes while the model folder grows. Growth or *.incomplete files
// mean the CLI is downloading; once they stop, the phase falls back to whatever the CLI output says.
func watchModelDownload(engine string, watch modelWatch, done <-chan struct{}) {
	if watch.root == "" {
		return
	}
	var expected atomic.Int64
	if watch.repo != "" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
		go func() { expected.Store(repoDownloadSize(ctx, watch.repo, watch.subdir)) }()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := cacheSize(watch.root)
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			size := cacheSize(watch.root)
			downloading := size > last || hasIncompleteFile(watch.root)
			last = size
			total := expected.Load()
			if total > 0 && size > total {
				total = 0 // stale numbers (model changed upstream): better unknown than >100%
			}
			updateProgress(engine, func(p *Progress) {
				p.ModelPath = watch.root
				p.DownloadedBytes = size
				p.TotalBytes = total
				switch {
				case downloading && (p.Phase == PhasePreparing || p.Phase == PhaseDownloading || p.Phase == PhaseLoading):
					p.Phase = PhaseDownloading
					if total > 0 {
						p.Percent = float64(min(99, int(size*100/total)))
					}
				case p.Phase == PhaseDownloading:
					// Download finished: the model is now being loaded / compiled.
					p.Phase = PhaseLoading
					p.Percent = 0
				}
			})
		}
	}
}
