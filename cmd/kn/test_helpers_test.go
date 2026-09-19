package main

import (
	"io"
	"os"
	"testing"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	origOut := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origOut }()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(r)
		done <- result{data, err}
	}()
	fn()
	w.Close()
	out := <-done
	if out.err != nil {
		t.Fatal(out.err)
	}
	return string(out.data)
}
