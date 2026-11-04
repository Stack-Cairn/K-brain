package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Stack-Cairn/K-brain/internal/computer/driver"
)

func main() {
	token := os.Getenv("K_BRAIN_COMPUTER_TOKEN")
	// Helpers this process starts (the desktop driver's Python) must not inherit it.
	_ = os.Unsetenv("K_BRAIN_COMPUTER_TOKEN")
	server := driver.New(driver.NewBackend(), token)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
