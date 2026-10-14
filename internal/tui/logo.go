package tui

import (
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	logoCanvasWidth  = 40
	logoCanvasHeight = 18
	logoTickInterval = 80 * time.Millisecond
)

type logoTickMsg struct{}

func logoTickCmd() tea.Cmd {
	return tea.Tick(logoTickInterval, func(time.Time) tea.Msg { return logoTickMsg{} })
}

// logoRamp orders face fill glyphs from dim to bright, as in the classic
// spinning donut. Heavier glyphs are reserved for edges and logo marks.
const logoRamp = ".,-~:;=!*"

// Face pattern marks, from the a2gent.net logo plus cube edges. Higher values
// win when several cover the same terminal cell.
const (
	logoMarkFill = iota
	logoMarkDot
	logoMarkCenter
	logoMarkEdge
)

var (
	// Face fill shades, dark to light blue.
	logoFillStyles = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("#123C8C")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#1450B4")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#1F6FEB")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#3D8BFF")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#5FA8FF")),
	}
	logoDotStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#B8DBFF")).Bold(true)
	logoCenterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	logoEdgeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#D6EAFF")).Bold(true)
)

type logoVec struct{ x, y, z float64 }

func (v logoVec) dot(o logoVec) float64 { return v.x*o.x + v.y*o.y + v.z*o.z }

// rotate spins around X, then Y, then Z so the cube tumbles like the donut.
func (v logoVec) rotate(a, b, c float64) logoVec {
	y := v.y*math.Cos(a) - v.z*math.Sin(a)
	z := v.y*math.Sin(a) + v.z*math.Cos(a)
	x := v.x*math.Cos(b) + z*math.Sin(b)
	z = -v.x*math.Sin(b) + z*math.Cos(b)
	return logoVec{x*math.Cos(c) - y*math.Sin(c), x*math.Sin(c) + y*math.Cos(c), z}
}

// unrotate is the inverse of rotate: it maps view space back to cube space.
func (v logoVec) unrotate(a, b, c float64) logoVec {
	x := v.x*math.Cos(c) + v.y*math.Sin(c)
	y := -v.x*math.Sin(c) + v.y*math.Cos(c)
	nx := x*math.Cos(b) - v.z*math.Sin(b)
	z := x*math.Sin(b) + v.z*math.Cos(b)
	return logoVec{nx, y*math.Cos(a) + z*math.Sin(a), -y*math.Sin(a) + z*math.Cos(a)}
}

// logoFaceMark maps face coords (u, v in [-1, 1]) to the a2gent.net pattern:
// four dots around a center square, framed by edges. The logo lattice is left
// out: at terminal resolution it turns faces into noise.
func logoFaceMark(u, v float64) int {
	au, av := math.Abs(u), math.Abs(v)
	switch {
	case au > 0.94 || av > 0.94: // thin, as both adjacent faces draw an edge
		return logoMarkEdge
	case au < 0.2 && av < 0.2:
		return logoMarkCenter
	case math.Hypot(au-0.7, av-0.7) < 0.12:
		return logoMarkDot
	}
	return logoMarkFill
}

// logoHit casts a ray from the camera through a screen offset and returns the
// shading and pattern mark of the cube face it hits first.
func logoHit(dx, dy, a, b, c float64, light logoVec) (lum float64, mark int, ok bool) {
	const distance = 5.0
	origin := logoVec{0, 0, -distance}.unrotate(a, b, c)
	dir := logoVec{dx, dy, 1}.unrotate(a, b, c)
	o := [3]float64{origin.x, origin.y, origin.z}
	d := [3]float64{dir.x, dir.y, dir.z}

	// Slab test against the [-1, 1] cube; the axis that sets tNear is the face.
	tNear, tFar, axis := math.Inf(-1), math.Inf(1), 0
	for i := range 3 {
		if math.Abs(d[i]) < 1e-12 {
			if math.Abs(o[i]) > 1 {
				return 0, 0, false
			}
			continue
		}
		t1, t2 := (-1-o[i])/d[i], (1-o[i])/d[i]
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tNear {
			tNear, axis = t1, i
		}
		tFar = math.Min(tFar, t2)
	}
	if tNear > tFar || tNear <= 0 {
		return 0, 0, false
	}

	var hit, normal [3]float64
	for i := range 3 {
		hit[i] = o[i] + d[i]*tNear
	}
	normal[axis] = -math.Copysign(1, d[axis])
	n := logoVec{normal[0], normal[1], normal[2]}.rotate(a, b, c)
	lum = 0.12 + 0.88*math.Max(0, n.dot(light)) // ambient keeps shadowed faces visible
	return lum, logoFaceMark(hit[(axis+1)%3], hit[(axis+2)%3]), true
}

// logoSubsamples are per-cell ray offsets; they smooth the silhouette and keep
// thin edges from aliasing away on the coarse terminal grid.
var logoSubsamples = [][2]float64{{-0.25, -0.25}, {0.25, -0.25}, {-0.25, 0.25}, {0.25, 0.25}}

// renderLogoCells ray-casts a shaded cube with the a2gent.net pattern on its
// faces, lit like the classic spinning donut. Empty cells are "" so callers
// can composite the cube over a background.
func renderLogoCells(frame int) [][]string {
	w, h := logoCanvasWidth, logoCanvasHeight
	t := float64(frame) + 20 // start on an angle instead of a flat front face
	a, b, c := t*0.07, t*0.05, t*0.03
	light := logoVec{-0.4, 0.6, -0.7} // upper left, towards the viewer
	n := math.Sqrt(light.dot(light))
	light = logoVec{light.x / n, light.y / n, light.z / n}

	const (
		scaleX = 50.0
		scaleY = 23.5 // terminal cells are about twice as tall as wide
	)
	cx, cy := float64(w-1)/2, float64(h-1)/2

	cells := make([][]string, h)
	for y := range cells {
		cells[y] = make([]string, w)
		for x := range cells[y] {
			hits, lumSum := 0, 0.0
			var marks [logoMarkEdge + 1]int
			for _, s := range logoSubsamples {
				lum, m, ok := logoHit((float64(x)+s[0]-cx)/scaleX, -(float64(y)+s[1]-cy)/scaleY, a, b, c, light)
				if ok {
					hits++
					lumSum += lum
					marks[m]++
				}
			}
			// Half-covered cells form the silhouette; fewer hits are left empty.
			if hits < 2 {
				continue
			}
			// The strongest pattern feature wins if it covers half of the cell.
			mark := 0
			for m := len(marks) - 1; m > 0; m-- {
				if marks[m]*2 >= hits {
					mark = m
					break
				}
			}
			cells[y][x] = logoGlyph(lumSum/float64(hits), mark)
		}
	}
	return cells
}

// logoGlyph shades faces by brightness and gives edges and logo marks their
// own heavy glyphs, so the cube shape reads clearly at terminal resolution.
func logoGlyph(lum float64, mark int) string {
	lum = math.Min(lum, 0.999)
	switch mark {
	case logoMarkEdge:
		return logoEdgeStyle.Render("#")
	case logoMarkDot:
		return logoDotStyle.Render("$")
	case logoMarkCenter:
		return logoCenterStyle.Render("@")
	}
	glyph := string(logoRamp[int(lum*float64(len(logoRamp)))])
	return logoFillStyles[int(lum*float64(len(logoFillStyles)))].Render(glyph)
}

// renderLogo renders the cube on its own, with spaces for empty cells.
func renderLogo(frame int) string {
	var out strings.Builder
	for _, row := range renderLogoCells(frame) {
		for _, cell := range row {
			if cell == "" {
				cell = " "
			}
			out.WriteString(cell)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

var matrixGlyphs = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ#$%*+=-:;."

var (
	matrixGreenBright = lipgloss.NewStyle().Foreground(lipgloss.Color("#45FF78"))
	matrixGreen       = lipgloss.NewStyle().Foreground(lipgloss.Color("#14A844"))
	matrixGreenDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#075326"))
	matrixBlue        = lipgloss.NewStyle().Foreground(lipgloss.Color("#168BFF"))
)

// renderMatrixBackground returns independently timed ASCII streams. Per-column
// speed, trail, pause, and glyph sequence stay stable as each stream falls.
func renderMatrixBackground(width, height, frame int) [][]string {
	if width < 1 || height < 1 {
		return nil
	}
	cells := make([][]string, height)
	tick := uint64(frame)
	for row := range cells {
		cells[row] = make([]string, width)
		for col := 0; col < width; col++ {
			cells[row][col] = " "

			seed := matrixHash(uint64(col+1), 0, 0)
			if seed%100 >= 72 {
				continue
			}

			speed := 1 + int((seed>>8)%3)
			trail := 4 + int((seed>>16)%8)
			gap := 5 + int((seed>>24)%uint64(max(6, height/2)))
			period := height + trail + gap
			offset := int(matrixHash(seed, 1, 2) % uint64(period))
			progress := int(tick)/speed + offset
			phase := progress % period
			head := phase - trail
			distance := head - row
			if distance < 0 || distance >= trail {
				continue
			}

			cycle := uint64(progress / period)
			glyphIndex := matrixHash(seed, cycle, uint64(distance)) % uint64(len(matrixGlyphs))
			glyph := string(matrixGlyphs[int(glyphIndex)])
			switch {
			case matrixHash(seed, cycle, uint64(distance+97))%131 == 0:
				cells[row][col] = matrixBlue.Render(glyph)
			case distance == 0:
				cells[row][col] = matrixGreenBright.Render(glyph)
			case distance < 3:
				cells[row][col] = matrixGreen.Render(glyph)
			default:
				cells[row][col] = matrixGreenDim.Render(glyph)
			}
		}
	}
	return cells
}

func matrixHash(a, b, c uint64) uint64 {
	x := a*0x9e3779b97f4a7c15 ^ b*0xbf58476d1ce4e5b9 ^ c*0x94d049bb133111eb
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ (x >> 31)
}
