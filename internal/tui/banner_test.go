package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestBannerBoxFitsEveryWidth(t *testing.T) {
	m := compactCmdModel()
	m.modelName, m.provName = "kimi-k3-fast", "inference"
	body := m.bannerBody()

	for _, width := range []int{8, 12, 20, 23, 24, 40, 80, 200} {
		out := renderBannerBox(body, width)
		lines := strings.Split(ansi.Strip(out), "\n")
		for _, line := range lines {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width %d: banner overflows (%d): %q", width, got, line)
			}
		}
		want := len(m.bannerRows())
		if width < 24 {
			if len(lines) != want {
				t.Fatalf("width %d: borderless fallback should keep %d rows, got %d", width, want, len(lines))
			}
			continue
		}
		if len(lines) != want+2 {
			t.Fatalf("width %d: bordered banner should be %d rows plus 2 border rows, got %d:\n%s", width, want, len(lines), out)
		}
		// The card is centred, so the border rows carry a leading indent.
		if !strings.HasPrefix(strings.TrimSpace(lines[0]), "╭") || !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "╰") {
			t.Fatalf("width %d: banner lost its rounded border:\n%s", width, out)
		}
	}
}

func TestBannerBoxSizesToContent(t *testing.T) {
	m := compactCmdModel()
	out := renderBannerBox(m.bannerBody(), 200)
	// The box itself is centred, so measure the trimmed row.
	got := lipgloss.Width(strings.TrimSpace(ansi.Strip(strings.Split(out, "\n")[0])))
	if got >= 200 {
		t.Fatalf("banner should size to its content, not the terminal: %d cells", got)
	}
	if got < bannerMinWidth {
		t.Fatalf("banner should not collapse below %d cells, got %d", bannerMinWidth, got)
	}
}

func TestBannerBoxGrowsWithLongRoute(t *testing.T) {
	m := compactCmdModel()
	m.modelName, m.provName = "claude-opus-5-1m-preview", "anthropic-vertex"
	out := renderBannerBox(m.bannerBody(), 200)
	plain := ansi.Strip(out)
	if got := lipgloss.Width(strings.TrimSpace(strings.Split(plain, "\n")[0])); got <= bannerMinWidth {
		t.Fatalf("a long route should widen the card past the floor, got %d", got)
	}
	if !strings.Contains(plain, "claude-opus-5-1m-preview@anthropic-vertex") {
		t.Fatalf("long route should not be truncated on a wide terminal:\n%s", plain)
	}
}

func TestBannerShowsModelHint(t *testing.T) {
	m := compactCmdModel()
	body := ansi.Strip(m.bannerBody())
	if !strings.Contains(body, "/model to change") {
		t.Errorf("banner should point at /model:\n%s", body)
	}
	for _, want := range []string{"Model:", "Directory:"} {
		if !strings.Contains(body, want) {
			t.Errorf("banner labels should carry a colon, missing %q:\n%s", want, body)
		}
	}
}

func TestBannerShowsRouteAndDirectory(t *testing.T) {
	m := compactCmdModel()
	m.modelName, m.provName = "kimi-k3-fast", "inference"
	body := ansi.Strip(m.bannerBody())
	for _, want := range []string{m.bannerName(), versionLabel(), "kimi-k3-fast@inference", shortCWD()} {
		if !strings.Contains(body, want) {
			t.Errorf("banner missing %q:\n%s", want, body)
		}
	}
}

func TestClearReseedsBanner(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendAssistantBlock("some reply")
	m.command("/clear")

	if len(m.blocks) == 0 || m.blocks[0].kind != blockBanner {
		t.Fatalf("/clear should leave the banner as block 0, got %d blocks", len(m.blocks))
	}
	if out := ansi.Strip(m.View()); strings.Contains(out, "some reply") {
		t.Fatalf("/clear kept the old transcript: %q", out)
	}
}

func TestResumeKeepsBannerAboveRestoredTranscript(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.store = st

	id, err := st.Create("/tmp", m.modelName, m.provName)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "restored question", Authored: true},
	}
	if err := st.Save(id, 1, msgs, m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}
	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}

	if len(m.blocks) < 3 || m.blocks[0].kind != blockBanner {
		t.Fatalf("resume should start the transcript with the banner, got %d blocks", len(m.blocks))
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockUser || !strings.Contains(last.text, "restored question") {
		t.Fatalf("restored history should follow the banner, got kind=%d text=%q", last.kind, last.text)
	}
}

func TestRewindRebuildKeepsBannerFirst(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.rebuildTranscript()
	if len(m.blocks) == 0 || m.blocks[0].kind != blockBanner {
		t.Fatalf("rebuilt transcript should start with the banner, got %d blocks", len(m.blocks))
	}
}

func TestBannerReadyRowReflectsStats(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	m := compactCmdModel()
	m.stats = bannerStats{skills: 12, mcpReady: 3, mcpTools: 28}
	card := ansi.Strip(strings.Join(m.bannerRows(), "\n"))
	for _, want := range []string{"12 skills", "3 MCP (28 tools)"} {
		if !strings.Contains(card, want) {
			t.Errorf("card missing %q:\n%s", want, card)
		}
	}

	// A failed server is the abnormal value, so it is what earns colour.
	m.stats = bannerStats{skills: 1, mcpReady: 1, mcpFailed: 2, mcpTools: 4}
	row := bannerRowContaining(t, m, "MCP")
	if !strings.Contains(ansi.Strip(row), "2/3 MCP \u2717") {
		t.Errorf("failed servers should dominate the readout: %q", ansi.Strip(row))
	}
	if !strings.Contains(row, errStyle.Render("2/3 MCP \u2717")) {
		t.Errorf("the failure should be painted with the error colour: %q", row)
	}
}

// bannerRowContaining returns the card row whose text holds needle.
func bannerRowContaining(t *testing.T, m *model, needle string) string {
	t.Helper()
	for _, r := range m.bannerRows() {
		if strings.Contains(ansi.Strip(r), needle) {
			return r
		}
	}
	t.Fatalf("no card row contains %q", needle)
	return ""
}

func TestBannerColoursOnlyWhatMatters(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	m := compactCmdModel()

	// /model is the one actionable span in the card, so it is the one accented.
	if row := bannerRowContaining(t, m, "/model"); !strings.Contains(row, accentStyle.Render("/model")) {
		t.Errorf("/model should be accented: %q", row)
	}

	// The model name is the row's one bright span; the provider only qualifies
	// it, so it drops a level rather than taking a colour of its own.
	row := bannerRowContaining(t, m, m.modelName)
	if !strings.Contains(row, chromeStyle.Render(m.modelName)) {
		t.Errorf("the model name should keep the value colour: %q", row)
	}
	if !strings.Contains(row, dimStyle.Render("@"+m.provName)) {
		t.Errorf("the provider should drop to faint: %q", row)
	}
	if strings.Contains(row, accentStyle.Render(m.provName)) {
		t.Errorf("the provider must not take the accent — that is /model's: %q", row)
	}

	// The effort lives in the footer, accented. Repeating it here would crowd
	// the row and duplicate what already has a better home.
	m.agent.Effort = "high"
	if card := ansi.Strip(strings.Join(m.bannerRows(), "\n")); strings.Contains(card, "high") {
		t.Errorf("the card should not carry the reasoning effort:\n%s", card)
	}

	// The brand name is bold structure, not a signal: the logo carries the colour.
	brand := bannerRowContaining(t, m, m.bannerName())
	if strings.Contains(brand, accentStyle.Render(m.bannerName())) {
		t.Errorf("the brand name should not take the accent: %q", brand)
	}
	if !strings.Contains(brand, brandStyle.Render(m.bannerName())) {
		t.Errorf("the brand name should be bold muted: %q", brand)
	}
}

// The mark keeps the product logo's own gradient rather than the UI accent.
func TestBannerLogoUsesBrandGradient(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	rows := bannerLogoRows()
	if len(rows) < 3 {
		t.Fatalf("the mark should be several rows tall, got %d", len(rows))
	}
	joined := strings.Join(rows, "\n")
	for name, c := range map[string]lipgloss.CompleteAdaptiveColor{
		"stem": logoStemColor, "upper": logoUpperColor, "lower": logoLowerColor,
	} {
		// Compare the escape prefix, not a whole rendered span: the arm segments
		// carry different leading padding on each row.
		sgr, _, _ := strings.Cut(lipgloss.NewStyle().Foreground(c).Render("x"), "x")
		if !strings.Contains(joined, sgr) {
			t.Errorf("the %s segment is missing its colour %q:\n%q", name, sgr, joined)
		}
	}
	// Every row must be the same width or the text column beside it goes ragged.
	for i, r := range rows {
		if got := lipgloss.Width(ansi.Strip(r)); got != bannerLogoWidth {
			t.Errorf("logo row %d is %d cells, want %d: %q", i, got, bannerLogoWidth, ansi.Strip(r))
		}
	}
}

// The mark and the data column are set side by side, so every card row has to
// start with the logo gutter and stay rectangular.
func TestBannerLogoAlignsWithDataRows(t *testing.T) {
	m := compactCmdModel()
	for _, stats := range []bannerStats{{}, {skills: 12, mcpReady: 3, mcpTools: 28}} {
		m.stats = stats
		rows := m.bannerRows()
		if len(rows) < len(bannerLogo) {
			t.Fatalf("card should be at least as tall as the mark: %d < %d", len(rows), len(bannerLogo))
		}
		for i, r := range rows {
			// Slice by rune: a block glyph is three bytes wide.
			runes := []rune(ansi.Strip(r))
			if len(runes) < bannerLogoWidth {
				t.Fatalf("row %d is shorter than the logo gutter: %q", i, string(runes))
			}
			if gutter := string(runes[:bannerLogoWidth]); strings.Trim(gutter, "█ ") != "" {
				t.Errorf("row %d does not start with the logo gutter: %q", i, gutter)
			}
		}
	}
}

// The card is painted whole in a single frame. Revealing it row by row read as
// a startup flicker, so bannerBody must never emit a partial card.
func TestBannerBodyIsWholeInOneFrame(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 24))

	body := m.bannerBody()
	rows := m.bannerRows()
	if got, want := len(strings.Split(body, "\n")), len(rows); got != want {
		t.Fatalf("bannerBody rendered %d rows, want all %d", got, want)
	}
	for i, r := range rows {
		if !strings.Contains(body, r) {
			t.Errorf("row %d missing from the rendered card: %q", i, ansi.Strip(r))
		}
	}
	if body != m.bannerBody() {
		t.Error("rendering the card twice should be identical — no animation state")
	}
}

// The product is K-brain in English and 氪脑 in Chinese, never both at once.
func TestBannerNameIsTranslated(t *testing.T) {
	m := compactCmdModel()
	for language, want := range map[string]string{"en": "K-brain", "zh_cn": "氪脑", "zh_Hant": "氪腦"} {
		m.cfg.Language = language
		if got := m.bannerName(); got != want {
			t.Errorf("%s wordmark = %q, want %q", language, got, want)
		}
		card := ansi.Strip(strings.Join(m.bannerRows(), "\n"))
		if !strings.Contains(card, want) {
			t.Errorf("%s card missing the wordmark %q:\n%s", language, want, card)
		}
		if language != "en" && strings.Contains(card, "K-brain ·") {
			t.Errorf("%s card still carries the bilingual wordmark:\n%s", language, card)
		}
	}
}

// The labels are capitalised in English and the two data rows are separated.
func TestBannerLabelsAndSpacing(t *testing.T) {
	m := compactCmdModel()
	card := ansi.Strip(strings.Join(m.bannerRows(), "\n"))
	for _, want := range []string{"Model:", "Directory:"} {
		if !strings.Contains(card, want) {
			t.Errorf("card missing capitalised label %q:\n%s", want, card)
		}
	}

	rows := m.bannerRows()
	modelRow, dirRow := -1, -1
	for i, r := range rows {
		switch {
		case strings.Contains(ansi.Strip(r), "Model:"):
			modelRow = i
		case strings.Contains(ansi.Strip(r), "Directory:"):
			dirRow = i
		}
	}
	if modelRow < 0 || dirRow < 0 {
		t.Fatalf("rows not found (model=%d dir=%d)", modelRow, dirRow)
	}
	if dirRow-modelRow < 2 {
		t.Errorf("want a blank row between Model and Directory, got rows %d and %d", modelRow, dirRow)
	}
}
