package tools

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

func IsBinary(data []byte) bool { return isBinary(data) }

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	if bytes.IndexByte(data, 0x00) >= 0 {
		return true
	}

	if !utf8.Valid(data) {
		return true
	}

	ctrl := 0
	for _, b := range data {
		switch {
		case b == 0x1b:

		case b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != 0x0b && b != 0x0c:
			ctrl++
		}
	}
	return ctrl*10 > len(data)
}

func bytesHuman(n int) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

func BinaryPlaceholder(name string, size int) string { return binaryPlaceholder(name, size) }

func binaryPlaceholder(name string, size int) string {
	if name == "" {
		return fmt.Sprintf("[binary output: %s, not shown]", bytesHuman(size))
	}
	return fmt.Sprintf("[binary: %s, %s]", name, bytesHuman(size))
}
