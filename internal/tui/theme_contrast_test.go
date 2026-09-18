package tui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func firstContentFG(l string) int {

	fg := -1
	i := 0
	for i < len(l) {
		if l[i] == 0x1b {
			end := strings.IndexByte(l[i:], 'm')
			if end < 0 {
				break
			}
			seq := l[i : i+end+1]
			var n int
			if _, err := fmt.Sscanf(seq, "\x1b[38;5;%dm", &n); err == nil {
				fg = n
			}
			i += end + 1
			continue
		}

		end := strings.IndexByte(l[i:], 0x1b)
		if end < 0 {
			end = len(l) - i
		}

		if strings.Trim(l[i:i+end], " \t") != "" && fg >= 0 {
			return fg
		}
		i += end
	}
	return -1
}

func sgrFG(s string) int {
	for i := 0; i+6 < len(s); i++ {
		if strings.HasPrefix(s[i:], "\x1b[38;5;") {
			var n int
			if _, err := fmt.Sscanf(s[i:], "\x1b[38;5;%dm", &n); err == nil {
				return n
			}
		}
	}
	return -1
}

func ansiLuminance(n int) float64 {
	if n < 0 {
		return -1
	}
	if n >= 232 {
		return float64(n-232)/23*0.9 + 0.05
	}
	if n >= 16 {
		n -= 16
		r, g, b := float64(n/36)/5, float64((n/6)%6)/5, float64(n%6)/5
		return 0.2126*r + 0.7152*g + 0.0722*b
	}
	std := []float64{0, .25, .2, .35, .15, .3, .4, .55, .4, .7, .65, .8, .5, .7, .8, .95}
	return std[n%16]
}

func TestMarkdownContrastBothThemes(t *testing.T) {
	samples := map[string]string{
		"body":      "plain body text",
		"bold":      "**bold text**",
		"inline":    "has `inline code` here",
		"list":      "- item one\n- item two",
		"codeblock": "```go\nfmt.Println(1)\n```",
	}
	for _, light := range []bool{true, false} {
		SetLightTheme(light)
		bgLum := 0.05
		if light {
			bgLum = 1.0
		}
		theme := map[bool]string{true: "light", false: "dark"}[light]
		for name, src := range samples {
			out := renderMarkdown(src, 70)
			for l := range strings.SplitSeq(out, "\n") {
				plain := ansi.Strip(l)
				if strings.TrimSpace(plain) == "" {
					continue
				}

				if strings.Trim(plain, " ░│─╌┃·") == "" {
					continue
				}
				fg := firstContentFG(l)
				if fg < 0 {
					continue
				}
				gap := ansiLuminance(fg) - bgLum
				if gap < 0 {
					gap = -gap
				}
				need := 0.45
				if name == "inline" || name == "codeblock" {
					need = 0.25
				}
				if gap < need {
					t.Errorf("%s theme %q: fg=%d luminance gap %.2f < %.2f (line %q)", theme, name, fg, gap, need, plain)
				}
				break
			}
		}
	}
	SetLightTheme(false)
}

func TestInlineCodeLightChip(t *testing.T) {
	SetLightTheme(true)
	defer SetLightTheme(false)
	out := renderMarkdown("use `config.Save` here", 60)
	if !strings.Contains(out, "48;5;255") {
		t.Errorf("light inline code should sit on a 255 chip: %q", out)
	}
	if !strings.Contains(out, "38;5;124") {
		t.Errorf("light inline code text should be dark red 124: %q", out)
	}
}

func ansiColorToRGB(n int, light bool) color.RGBA {
	if n < 0 {
		if light {
			return color.RGBA{40, 40, 40, 255}
		}
		return color.RGBA{220, 220, 220, 255}
	}
	if n >= 232 {
		v := uint8(8 + (n-232)*10)
		return color.RGBA{v, v, v, 255}
	}
	if n >= 16 {
		n -= 16
		lv := [6]uint8{0, 95, 135, 175, 215, 255}
		return color.RGBA{lv[n/36], lv[(n/6)%6], lv[n%6], 255}
	}
	std := [16]color.RGBA{
		{0, 0, 0, 255},
		{128, 0, 0, 255},
		{0, 128, 0, 255},
		{128, 128, 0, 255},
		{0, 0, 128, 255},
		{128, 0, 128, 255},
		{0, 128, 128, 255},
		{192, 192, 192, 255},
		{128, 128, 128, 255},
		{255, 0, 0, 255},
		{0, 255, 0, 255},
		{255, 255, 0, 255},
		{0, 0, 255, 255},
		{255, 0, 255, 255},
		{0, 255, 255, 255},
		{255, 255, 255, 255},
	}
	return std[n%16]
}

func TestThemeProbeImages(t *testing.T) {
	const cellW, cellH, cols = 8, 14, 72
	sample := "I found the bug and **fixed** it.\n\n1. isolate `HOME` in tests\n2. add a `K_BRAIN_HOME` override\n\n```go\nos.Setenv(\"K_BRAIN_HOME\", dir)\n```\n\n| file | change |\n|---|---|\n| config.go | Dir() override |"
	for _, light := range []bool{false, true} {
		SetLightTheme(light)
		lines := strings.Split(renderMarkdown(sample, cols-4), "\n")
		img := image.NewRGBA(image.Rect(0, 0, cols*cellW, (len(lines)+2)*cellH))
		bg := color.RGBA{30, 30, 34, 255}
		name := "/tmp/k-brain-theme-dark.png"
		if light {
			bg = color.RGBA{255, 255, 255, 255}
			name = "/tmp/k-brain-theme-light.png"
		}
		for y := range img.Bounds().Dy() {
			for x := range img.Bounds().Dx() {
				img.Set(x, y, bg)
			}
		}
		for row, l := range lines {
			fg := ansiColorToRGB(sgrFG(l), light)
			for col, r := range []rune(ansi.Strip(l)) {
				if r == ' ' || col >= cols {
					continue
				}
				x0, y0 := col*cellW, (row+1)*cellH
				for y := y0 + 3; y < y0+cellH-3; y++ {
					for x := x0 + 1; x < x0+cellW-1; x++ {
						img.Set(x, y, fg)
					}
				}
			}
		}
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		t.Logf("wrote %s", name)
	}
	SetLightTheme(false)
}
