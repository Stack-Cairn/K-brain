package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// useProfile switches the global lipgloss renderer to profile and background
// and rebuilds the styles; the returned func puts everything back. Tests that
// use it touch process-wide state and must not call t.Parallel.
func useProfile(t *testing.T, profile termenv.Profile, dark bool) func() {
	t.Helper()
	prevProfile, prevDark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	prevMono, prevASCII, prevRaised := monoMode, asciiMode, monoRaised
	lipgloss.SetColorProfile(profile)
	lipgloss.SetHasDarkBackground(dark)
	buildStyles()
	invalidateMDRenderer()
	return func() {
		lipgloss.SetColorProfile(prevProfile)
		lipgloss.SetHasDarkBackground(prevDark)
		monoMode, monoRaised = prevMono, prevRaised
		setASCIIGlyphs(prevASCII)
		invalidateMDRenderer()
	}
}
