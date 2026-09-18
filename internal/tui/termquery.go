package tui

import (
	"regexp"
	"strconv"
	"strings"
)

var cprRE = regexp.MustCompile(`\x1b\[[0-9]+;[0-9]+R`)

func bgQuery(inTmux bool) string {
	const osc11 = "\x1b]11;?\x1b\\"
	q := osc11
	if inTmux {
		q += "\x1bPtmux;" + strings.ReplaceAll(osc11, "\x1b", "\x1b\x1b") + "\x1b\\"
	}

	return q + "\x1b[?996n\x1b[6n"
}

func parseOSCBgRGB(payload string) (r, g, b int, ok bool) {
	payload = strings.TrimSpace(payload)
	if after, found := strings.CutPrefix(payload, "rgb:"); found {
		parts := strings.Split(after, "/")
		if len(parts) != 3 {
			return 0, 0, 0, false
		}

		comp := func(s string) int {
			s = strings.TrimRight(s, "\x07")

			v, err := strconv.ParseUint(s, 16, 16)
			if err != nil {
				return 0
			}
			maxVal := (1 << (4 * uint(len(s)))) - 1
			if maxVal <= 0 {
				return 0
			}
			return int(v) * 255 / maxVal
		}
		return comp(parts[0]), comp(parts[1]), comp(parts[2]), true
	}
	if strings.HasPrefix(payload, "#") && len(payload) >= 7 {
		v, err := strconv.ParseUint(payload[1:7], 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff, true
	}
	return 0, 0, 0, false
}

func rgbIsLight(r, g, b int) bool {
	return (299*r+587*g+114*b)/1000 > 128
}

func parseOSCBg(payload string) bool {
	r, g, b, ok := parseOSCBgRGB(payload)
	return ok && rgbIsLight(r, g, b)
}
