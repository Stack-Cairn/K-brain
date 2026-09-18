package browser

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentSessionsDriveIsolatedBrowsers(t *testing.T) {
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	AllowPrivateURLs = true
	defer func() { AllowPrivateURLs = false }()
	url := testPage(t)

	m := NewManager(ModeHeadless)
	defer m.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const sessions = 3
	var wg sync.WaitGroup
	errs := make(chan error, sessions)
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess, err := m.Session(fmt.Sprintf("s%d", i))
			if err != nil {
				errs <- err
				return
			}

			marker := fmt.Sprintf("session-%d", i)
			out, err := sess.Do(ctx, func(b Backend) (string, error) {
				if err := b.Navigate(ctx, url+"/marker/"+marker); err != nil {
					return "", err
				}
				return b.Eval(ctx, "document.title")
			})
			if err != nil {
				errs <- fmt.Errorf("s%d: %w", i, err)
				return
			}
			if !strings.Contains(out, marker) {
				errs <- fmt.Errorf("s%d: session leaked — got title %s, want marker %s", i, out, marker)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestChurnOpenClose(t *testing.T) {
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	AllowPrivateURLs = true
	defer func() { AllowPrivateURLs = false }()
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	for i := range 10 {
		b, err := Open(ctx, ModeHeadless)
		if err != nil {
			t.Fatalf("iter %d open: %v", i, err)
		}
		if err := b.Navigate(ctx, url); err != nil {
			t.Fatalf("iter %d navigate: %v", i, err)
		}
		if _, err := b.Eval(ctx, "document.title"); err != nil {
			t.Fatalf("iter %d eval: %v", i, err)
		}
		if err := b.Close(); err != nil {
			t.Fatalf("iter %d close: %v", i, err)
		}
	}
}

func TestRecoverFromClosedBrowser(t *testing.T) {
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	AllowPrivateURLs = true
	defer func() { AllowPrivateURLs = false }()
	url := testPage(t)

	m := NewManager(ModeHeadless)
	defer m.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sess, err := m.Session("crashy")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sess.Do(ctx, func(b Backend) (string, error) {
		return "", b.Navigate(ctx, url)
	})
	if err != nil {
		t.Fatalf("initial: %v", err)
	}

	sess.drop()
	out, err := sess.Do(ctx, func(b Backend) (string, error) {
		if err := b.Navigate(ctx, url); err != nil {
			return "", err
		}
		return b.Eval(ctx, "document.title")
	})
	if err != nil {
		t.Fatalf("after crash: %v", err)
	}
	if !strings.Contains(out, "k-brain e2e") {
		t.Fatalf("post-crash eval: %s", out)
	}
}

func TestManySequentialCalls(t *testing.T) {
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	AllowPrivateURLs = true
	defer func() { AllowPrivateURLs = false }()
	url := testPage(t)

	m := NewManager(ModeHeadless)
	defer m.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	sess, err := m.Session("longhaul")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sess.Do(ctx, func(b Backend) (string, error) {
		return "", b.Navigate(ctx, url)
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		_, err := sess.Do(ctx, func(b Backend) (string, error) {
			r, err := b.Eval(ctx, fmt.Sprintf("%d+1", i))
			if err == nil && r != strconv.Itoa(i+1) {
				return "", fmt.Errorf("iter %d: got %s", i, r)
			}
			return "", err
		})
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}
