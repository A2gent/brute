package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestLogoRowsAligned(t *testing.T) {
	want := len([]rune(logoRows[0]))
	for i, row := range logoRows {
		if got := len([]rune(row)); got != want {
			t.Fatalf("row %d width = %d, want %d", i, got, want)
		}
	}
}

func TestRenderLogoAnimates(t *testing.T) {
	// Tests run without a TTY, so force colors or all frames look identical.
	lipgloss.SetColorProfile(termenv.TrueColor)
	if renderLogo(0) == renderLogo(1) {
		t.Fatal("expected different frames to render differently")
	}
	if strings.Count(renderLogo(0), "\n") != len(logoRows) {
		t.Fatal("expected one line per logo row plus wordmark")
	}
}
