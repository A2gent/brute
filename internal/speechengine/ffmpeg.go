package speechengine

import "path/filepath"

// FFmpegPath is shared by diagnostics and audio conversion; GUI-launched Brute
// may not inherit Homebrew's bin directory in PATH.
func FFmpegPath() string { return resolveFFmpegForPlatform(currentPlatform()) }

func resolveFFmpegForPlatform(p *platformEnv) string {
	if path, err := p.lookPath("ffmpeg"); err == nil && pathIsExecutable(path) {
		return path
	}
	if isDarwin(p) {
		for _, dir := range brewBinDirsForPlatform(p) {
			path := filepath.Join(dir, "ffmpeg")
			if pathIsExecutable(path) {
				return path
			}
		}
	}
	return ""
}
