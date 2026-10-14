package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestRenderLogoCellsFitCanvas(t *testing.T) {
	for frame := 0; frame < 200; frame += 7 {
		cells := renderLogoCells(frame)
		if len(cells) != logoCanvasHeight {
			t.Fatalf("frame %d: height = %d, want %d", frame, len(cells), logoCanvasHeight)
		}
		drawn := 0
		for _, row := range cells {
			if len(row) != logoCanvasWidth {
				t.Fatalf("frame %d: width = %d, want %d", frame, len(row), logoCanvasWidth)
			}
			for _, cell := range row {
				if cell != "" {
					drawn++
				}
			}
		}
		// A visible cube covers a good part of the canvas at any angle.
		if drawn < logoCanvasWidth*logoCanvasHeight/5 {
			t.Fatalf("frame %d: only %d cells drawn", frame, drawn)
		}
	}
}

func TestRenderLogoAnimates(t *testing.T) {
	// Tests run without a TTY, so force colors to compare styled output.
	lipgloss.SetColorProfile(termenv.TrueColor)
	if renderLogo(0) == renderLogo(1) {
		t.Fatal("expected different frames to render differently")
	}
	if strings.Count(renderLogo(0), "\n") != logoCanvasHeight {
		t.Fatal("expected one line per canvas row")
	}
}
