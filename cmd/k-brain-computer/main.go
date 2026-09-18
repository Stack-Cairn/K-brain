package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Stack-Cairn/K-brain/internal/computer/driver"
)

func main() {
	server := driver.New(driver.NewBackend(), os.Getenv("K_BRAIN_COMPUTER_TOKEN"))
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
