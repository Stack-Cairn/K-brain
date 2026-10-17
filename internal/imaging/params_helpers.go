package imaging

import (
	"strconv"
	"strings"
)

func itoa(n int) string { return strconv.Itoa(n) }

// parseSize reads "WxH" (x, X or ×) into positive dimensions.
func parseSize(size string) (float64, float64, bool) {
	s := strings.TrimSpace(size)
	for _, sep := range []string{"x", "X", "×"} {
		if i := strings.Index(s, sep); i > 0 {
			w, e1 := strconv.Atoi(strings.TrimSpace(s[:i]))
			h, e2 := strconv.Atoi(strings.TrimSpace(s[i+len(sep):]))
			if e1 != nil || e2 != nil || w <= 0 || h <= 0 || w > 100000 || h > 100000 {
				return 0, 0, false
			}
			return float64(w), float64(h), true
		}
	}
	return 0, 0, false
}
