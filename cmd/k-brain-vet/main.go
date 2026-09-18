package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/Stack-Cairn/K-brain/internal/uilock"
)

func main() {
	multichecker.Main(uilock.Analyzer)
}
