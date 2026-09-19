package main

import (
	"context"
	"io"
	"os"
)

func readRunInput(ctx context.Context, input *os.File) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(input)
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		_ = input.Close()
		<-done
		return nil, ctx.Err()
	}
}
