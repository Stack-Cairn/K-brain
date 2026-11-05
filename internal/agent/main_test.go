package agent

import (
	"os"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/testhome"
)

func TestMain(m *testing.M) { os.Exit(testhome.Run(m)) }
