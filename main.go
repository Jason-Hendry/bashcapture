package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/term"
)

//go:embed JetBrainsMono-2.304/fonts/ttf/JetBrainsMono-Regular.ttf
var fontTTF []byte

const (
	fontSize   = 14.0
	lineHeight = 20
	padX       = 20
	padY       = 20
	tabWidth   = 8
)

var (
	bgColor     = color.RGBA{40, 44, 52, 255}   // dark background
	textColor   = color.RGBA{171, 178, 191, 255} // light gray text
	promptColor = color.RGBA{97, 175, 239, 255}  // blue for prompt

	// Standard ANSI colors (normal intensity)
	ansiColors = [8]color.RGBA{
		{0, 0, 0, 255},       // 0: black
		{224, 108, 117, 255}, // 1: red
		{152, 195, 121, 255}, // 2: green
		{229, 192, 123, 255}, // 3: yellow
		{97, 175, 239, 255},  // 4: blue
		{198, 120, 221, 255}, // 5: magenta
		{86, 182, 194, 255},  // 6: cyan
		{171, 178, 191, 255}, // 7: white
	}
	// Bright ANSI colors
	ansiBrightColors = [8]color.RGBA{
		{92, 99, 112, 255},   // 0: bright black
		{224, 108, 117, 255}, // 1: bright red
		{152, 195, 121, 255}, // 2: bright green
		{229, 192, 123, 255}, // 3: bright yellow
		{97, 175, 239, 255},  // 4: bright blue
		{198, 120, 221, 255}, // 5: bright magenta
		{86, 182, 194, 255},  // 6: bright cyan
		{255, 255, 255, 255}, // 7: bright white
	}

	ansiRe   = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	// Non-SGR sequences to strip:
	//  - CSI with optional ?/>/! prefix: \x1b[?2004h, \x1b[>c, \x1b[1K, etc.
	//  - OSC terminated by BEL or ST: \x1b]...(\x07|\x1b\\)
	//  - Two-char escapes: \x1b(B, \x1b=, \x1b>, \x1bM, etc.
	nonSGRRe = regexp.MustCompile(`\x1b\[[?>=!]?[0-9;]*[^m0-9;\x1b]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[^[\]0-9]`)
)

// colorSpan represents a run of text with a specific color.
type colorSpan struct {
	text  string
	color color.RGBA
}

// parseANSI splits a line into colored spans by interpreting ANSI SGR sequences.
func parseANSI(line string, defaultColor color.RGBA) []colorSpan {
	cur := defaultColor
	bold := false
	var spans []colorSpan

	indices := ansiRe.FindAllStringIndex(line, -1)
	if indices == nil {
		return []colorSpan{{text: line, color: cur}}
	}

	prev := 0
	for _, loc := range indices {
		if loc[0] > prev {
			spans = append(spans, colorSpan{text: line[prev:loc[0]], color: cur})
		}
		seq := line[loc[0]:loc[1]]
		cur, bold = applyANSI(seq, defaultColor, cur, bold)
		prev = loc[1]
	}
	if prev < len(line) {
		spans = append(spans, colorSpan{text: line[prev:], color: cur})
	}
	return spans
}

// applyANSI updates the current color based on an SGR escape sequence.
func applyANSI(seq string, defaultColor, cur color.RGBA, bold bool) (color.RGBA, bool) {
	// Strip \x1b[ and trailing m
	inner := seq[2 : len(seq)-1]
	if inner == "" {
		return defaultColor, false
	}
	parts := strings.Split(inner, ";")
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0: // reset
			cur = defaultColor
			bold = false
		case n == 1: // bold
			bold = true
		case n >= 30 && n <= 37: // foreground color
			if bold {
				cur = ansiBrightColors[n-30]
			} else {
				cur = ansiColors[n-30]
			}
		case n == 39: // default foreground
			cur = defaultColor
		case n >= 90 && n <= 97: // bright foreground
			cur = ansiBrightColors[n-90]
		case n == 38: // extended color (256/RGB)
			if i+1 < len(parts) {
				mode, _ := strconv.Atoi(parts[i+1])
				if mode == 5 && i+2 < len(parts) { // 256-color
					idx, _ := strconv.Atoi(parts[i+2])
					cur = color256(idx)
					i += 2
				} else if mode == 2 && i+4 < len(parts) { // 24-bit RGB
					r, _ := strconv.Atoi(parts[i+2])
					g, _ := strconv.Atoi(parts[i+3])
					b, _ := strconv.Atoi(parts[i+4])
					cur = color.RGBA{uint8(r), uint8(g), uint8(b), 255}
					i += 4
				}
			}
		}
	}
	return cur, bold
}

// color256 converts a 256-color index to RGBA.
func color256(idx int) color.RGBA {
	switch {
	case idx < 8:
		return ansiColors[idx]
	case idx < 16:
		return ansiBrightColors[idx-8]
	case idx < 232: // 6x6x6 color cube
		idx -= 16
		b := idx % 6
		idx /= 6
		g := idx % 6
		r := idx / 6
		return color.RGBA{uint8(r * 51), uint8(g * 51), uint8(b * 51), 255}
	default: // grayscale
		v := uint8(8 + (idx-232)*10)
		return color.RGBA{v, v, v, 255}
	}
}

// stripANSI removes all ANSI escape sequences from a string.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// cleanPTY strips carriage returns, non-SGR escape sequences, and
// leftover control characters from PTY output.
func cleanPTY(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")
	s = nonSGRRe.ReplaceAllString(s, "")
	// Strip any remaining non-printable control chars (except \n and \t and ESC for SGR)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\t' || c == '\x1b' || c >= 0x20 {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: bashcapture <command> [args...]\n")
		os.Exit(1)
	}

	cmdArgs := os.Args[1:]
	cmdStr := strings.Join(cmdArgs, " ")

	cmd := exec.Command("bash", "-ic", cmdStr)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty start error: %v\n", err)
		os.Exit(1)
	}
	defer ptmx.Close()

	// Handle SIGWINCH: resize PTY to match real terminal
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
				_ = pty.Setsize(ptmx, ws)
			}
		}
	}()
	ch <- syscall.SIGWINCH // initial sync

	// Put the real terminal into raw mode so keystrokes pass through
	if oldState, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()
	}

	// Pipe stdin → PTY
	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()

	// Tee PTY output → real stdout + capture buffer
	var capture bytes.Buffer
	_, _ = io.Copy(io.MultiWriter(os.Stdout, &capture), ptmx)

	_ = cmd.Wait()
	signal.Stop(ch)
	close(ch)

	output := expandTabs(cleanPTY(capture.String()))

	// Build lines: prompt + command, then output
	promptLine := "$ " + cmdStr
	lines := []string{promptLine}
	for _, l := range strings.Split(output, "\n") {
		lines = append(lines, l)
	}
	// Trim trailing empty lines
	for len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Keep only the last 1000 lines (plus the prompt line)
	const maxLines = 1000
	if len(lines) > maxLines+1 {
		lines = append(lines[:1], lines[len(lines)-maxLines:]...)
	}

	// Load font
	f, err := truetype.Parse(fontTTF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "font parse error: %v\n", err)
		os.Exit(1)
	}

	// Measure max line width (ignoring ANSI escape codes)
	opts := truetype.Options{Size: fontSize, DPI: 72}
	face := truetype.NewFace(f, &opts)
	maxWidth := 0
	for _, l := range lines {
		w := font.MeasureString(face, stripANSI(l)).Ceil()
		if w > maxWidth {
			maxWidth = w
		}
	}

	imgW := maxWidth + padX*2
	imgH := len(lines)*lineHeight + padY*2

	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))

	// Fill background
	for y := 0; y < imgH; y++ {
		for x := 0; x < imgW; x++ {
			img.Set(x, y, bgColor)
		}
	}

	// Draw text
	ctx := freetype.NewContext()
	ctx.SetDPI(72)
	ctx.SetFont(f)
	ctx.SetFontSize(fontSize)
	ctx.SetClip(img.Bounds())
	ctx.SetDst(img)

	for i, line := range lines {
		y := padY + (i+1)*lineHeight - 4
		x := padX

		var spans []colorSpan
		if i == 0 {
			// Prompt line: single color, no ANSI parsing
			spans = []colorSpan{{text: line, color: promptColor}}
		} else {
			spans = parseANSI(line, textColor)
		}

		for _, span := range spans {
			if span.text == "" {
				continue
			}
			ctx.SetSrc(image.NewUniform(span.color))
			pt := freetype.Pt(x, y)
			endPt, err := ctx.DrawString(span.text, pt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "draw error: %v\n", err)
				os.Exit(1)
			}
			x = endPt.X.Round()
		}
	}

	// Save
	home, _ := os.UserHomeDir()
	screenshotDir := home + "/Pictures/Screenshots"
	_ = os.MkdirAll(screenshotDir, 0755)
	filename := screenshotDir + "/" + time.Now().Format("2006-01-02T15:04:05") + ".png"
	outFile, err := os.Create(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create file error: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	if err := png.Encode(outFile, img); err != nil {
		fmt.Fprintf(os.Stderr, "png encode error: %v\n", err)
		os.Exit(1)
	}

}

func expandTabs(s string) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			for j := 0; j < spaces; j++ {
				b.WriteByte(' ')
			}
			col += spaces
		} else if r == '\n' {
			b.WriteRune(r)
			col = 0
		} else {
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}
