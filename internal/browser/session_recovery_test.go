package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/go-rod/rod/lib/cdp"
)

type recoveryBackend struct {
	fakeBackend
	prepare       func(context.Context) error
	prepared      int
	invalidations int
	invalid       error
	closes        int
	closeErr      error
}

func (b *recoveryBackend) Close() error {
	b.closes++
	b.fakeBackend.Close()
	return b.closeErr
}

func (b *recoveryBackend) Prepare(ctx context.Context) error {
	b.prepared++
	if b.prepare != nil {
		return b.prepare(ctx)
	}
	return nil
}

func (b *recoveryBackend) Invalidate(err error) {
	b.invalidations++
	b.invalid = err
}

func TestSessionCloseWaitsForActiveProgram(t *testing.T) {
	b := &recoveryBackend{}
	stubOpen(t, func(context.Context, Mode, string) (Backend, error) { return b, nil })
	m := NewManager(ModeLive)
	s, _ := m.Session("default")
	entered, release, finished, dropped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = s.Do(t.Context(), func(Backend) (string, error) {
			close(entered)
			<-release
			return "done", nil
		})
	}()
	<-entered
	go func() { m.CloseAll(); close(dropped) }()
	select {
	case <-dropped:
		close(release)
		<-finished
		t.Fatal("backend closed while a program was using it")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-finished
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("session close did not finish after program returned")
	}
	if !b.closed {
		t.Fatal("backend was not closed")
	}
}

func TestSessionDoFreshRequiresExplicitReset(t *testing.T) {
	for _, mode := range []Mode{ModeLive, ModeDedicated, ModeHeadless, ModeExtension} {
		t.Run(string(mode), func(t *testing.T) {
			old, fresh := &recoveryBackend{}, &recoveryBackend{}
			opens, scripts := 0, 0
			stubOpen(t, func(_ context.Context, got Mode, name string) (Backend, error) {
				if got != mode || name != "default" {
					t.Fatalf("reset lost identity: %s:%s", got, name)
				}
				opens++
				if opens == 1 {
					return old, nil
				}
				return fresh, nil
			})
			m := NewManager(mode)
			s, _ := m.Session("default")
			if _, err := s.Do(t.Context(), func(Backend) (string, error) { return "initial", nil }); err != nil {
				t.Fatal(err)
			}
			old.prepare = func(context.Context) error { return ErrTargetClosed }
			for range 2 {
				_, err := s.Do(t.Context(), func(Backend) (string, error) { scripts++; return "", nil })
				if !errors.Is(err, ErrTargetClosed) || opens != 1 || old.closes != 0 || scripts != 0 {
					t.Fatalf("default call changed closed target: err=%v opens=%d closes=%d scripts=%d", err, opens, old.closes, scripts)
				}
			}
			out, err := s.DoFresh(t.Context(), func(b Backend) (string, error) {
				scripts++
				if b != fresh || old.closes != 1 {
					t.Error("reset did not close old backend before executing fresh program")
				}
				return "fresh", nil
			})
			if err != nil || out != "fresh" || opens != 2 || scripts != 1 {
				t.Fatalf("fresh call: out=%q err=%v opens=%d scripts=%d", out, err, opens, scripts)
			}
			if again, err := m.Session("default"); err != nil || again != s {
				t.Fatalf("reset replaced session: %v", err)
			}
			if _, err := s.Do(t.Context(), func(b Backend) (string, error) {
				if b != fresh {
					t.Error("next call lost fresh binding")
				}
				return "same", nil
			}); err != nil || opens != 2 {
				t.Fatalf("next call reopened: %v opens=%d", err, opens)
			}
		})
	}
}

func TestSessionDoFreshWaitsForProgramAndHonorsCancellation(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelWait), func(t *testing.T) {
			old, fresh := &recoveryBackend{}, &recoveryBackend{}
			opens, scripts := 0, 0
			stubOpen(t, func(context.Context, Mode, string) (Backend, error) { opens++; return fresh, nil })
			s, _ := NewManager(ModeExtension).Session("original")
			s.backend = old
			entered, release := make(chan struct{}), make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				_, err := s.Do(t.Context(), func(Backend) (string, error) {
					close(entered)
					<-release
					return "done", nil
				})
				finished <- err
			}()
			<-entered
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reset := make(chan error, 1)
			go func() {
				_, err := s.DoFresh(ctx, func(b Backend) (string, error) {
					scripts++
					if b != fresh {
						t.Error("reset program used old backend")
					}
					return "fresh", nil
				})
				reset <- err
			}()
			select {
			case err := <-reset:
				close(release)
				<-finished
				t.Fatalf("reset did not wait: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if cancelWait {
				cancel()
				select {
				case err := <-reset:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("cancelled reset: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("reset ignored cancellation")
				}
			}
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if cancelWait {
				if old.closes != 0 || s.backend != old || opens != 0 || scripts != 0 {
					t.Fatalf("cancelled reset changed binding: closes=%d opens=%d scripts=%d", old.closes, opens, scripts)
				}
			} else {
				select {
				case err := <-reset:
					if err != nil || old.closes != 1 || s.backend != fresh || opens != 1 || scripts != 1 {
						t.Fatalf("reset after program: err=%v closes=%d opens=%d scripts=%d", err, old.closes, opens, scripts)
					}
				case <-time.After(time.Second):
					t.Fatal("reset did not finish after program")
				}
			}
		})
	}
}

func TestSessionDoFreshCanceledBeforeAcquiring(t *testing.T) {
	b := &recoveryBackend{}
	s := &Session{sem: make(chan struct{}, 1), backend: b}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.DoFresh(ctx, func(Backend) (string, error) { t.Error("cancelled reset ran program"); return "", nil })
	if !errors.Is(err, context.Canceled) || b.closes != 0 || s.backend != b {
		t.Fatalf("cancelled reset changed backend: err=%v closes=%d", err, b.closes)
	}
}

func TestSessionDoFreshCloseFailureKeepsBinding(t *testing.T) {
	cause := errors.New("detach failed")
	old, fresh := &recoveryBackend{closeErr: cause}, &recoveryBackend{}
	opens, scripts := 0, 0
	stubOpen(t, func(context.Context, Mode, string) (Backend, error) { opens++; return fresh, nil })
	s := &Session{sem: make(chan struct{}, 1), backend: old}
	_, err := s.DoFresh(t.Context(), func(Backend) (string, error) { scripts++; return "", nil })
	if !errors.Is(err, cause) || s.backend != old || old.closes != 1 || opens != 0 || scripts != 0 {
		t.Fatalf("reset close error changed binding: err=%v closes=%d opens=%d scripts=%d", err, old.closes, opens, scripts)
	}
	old.prepare = func(context.Context) error { return ErrTargetClosed }
	if _, err := s.Do(t.Context(), func(Backend) (string, error) { scripts++; return "", nil }); !errors.Is(err, ErrTargetClosed) || opens != 0 || scripts != 0 {
		t.Fatalf("ordinary call reopened after failed reset: err=%v opens=%d scripts=%d", err, opens, scripts)
	}
	old.closeErr = nil
	if _, err := s.DoFresh(t.Context(), func(Backend) (string, error) { scripts++; return "fresh", nil }); err != nil || opens != 1 || scripts != 1 || s.backend != fresh {
		t.Fatalf("explicit retry: err=%v opens=%d scripts=%d", err, opens, scripts)
	}
}

func TestSessionNeverReplaysInterruptedProgram(t *testing.T) {
	for _, cause := range []error{
		io.EOF,
		&cdp.Error{Code: -32001, Message: "Session with given id not found"},
		&cdp.Error{Code: -32001, Message: "Session with given id not found."},
		&cdproto.Error{Code: -32001, Message: "Session with given id not found"},
		&cdproto.Error{Code: -32001, Message: "Session with given id not found."},
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			b := &recoveryBackend{fakeBackend: fakeBackend{mode: ModeLive, obtained: ObtainedLive}}
			opens := 0
			stubOpen(t, func(context.Context, Mode, string) (Backend, error) {
				opens++
				return b, nil
			})
			s, _ := NewManager(ModeLive).Session("same-target")
			mutations := 0
			out, err := s.Do(t.Context(), func(Backend) (string, error) {
				mutations++
				return "submitted once\n", fmt.Errorf("second helper: %w", cause)
			})
			if mutations != 1 || opens != 1 {
				t.Fatalf("program replayed: mutations=%d opens=%d", mutations, opens)
			}
			if out != "submitted once\n" || !errors.Is(err, cause) {
				t.Fatalf("lost partial output or original error: out=%q err=%v", out, err)
			}
			if b.invalidations != 1 || b.closed {
				t.Fatalf("must invalidate without discarding target identity: invalidations=%d closed=%v", b.invalidations, b.closed)
			}
			b.prepare = func(context.Context) error { b.invalid = nil; return nil }
			out, err = s.Do(t.Context(), func(got Backend) (string, error) {
				if got != b || b.invalid != nil {
					t.Fatal("new call did not prepare the original backend")
				}
				return "new call", nil
			})
			if err != nil || out != "new call" || opens != 1 || b.prepared != 2 {
				t.Fatalf("next call recovery: out=%q err=%v opens=%d prepared=%d", out, err, opens, b.prepared)
			}
		})
	}
}

func TestSessionPreparationFailureDoesNotExecuteProgram(t *testing.T) {
	targetGone := errors.New("original target closed")
	b := &recoveryBackend{prepare: func(context.Context) error { return targetGone }}
	stubOpen(t, func(context.Context, Mode, string) (Backend, error) { return b, nil })
	s, _ := NewManager(ModeLive).Session("closed-target")
	_, err := s.Do(t.Context(), func(Backend) (string, error) {
		t.Error("program ran after preparation failed")
		return "", nil
	})
	if !errors.Is(err, targetGone) || b.closed {
		t.Fatalf("must retain original target failure: err=%v closed=%v", err, b.closed)
	}
}

func TestSessionRetriesOpenBeforeProgramOnly(t *testing.T) {
	opens := 0
	b := &recoveryBackend{fakeBackend: fakeBackend{mode: ModeLive, obtained: ObtainedLive}}
	stubOpen(t, func(context.Context, Mode, string) (Backend, error) {
		opens++
		if opens == 1 {
			return nil, io.EOF
		}
		return b, nil
	})
	s, _ := NewManager(ModeLive).Session("startup")
	calls := 0
	out, err := s.Do(t.Context(), func(Backend) (string, error) { calls++; return "ready", nil })
	if err != nil || out != "ready" || opens != 2 || calls != 1 {
		t.Fatalf("startup retry: out=%q err=%v opens=%d calls=%d", out, err, opens, calls)
	}
}

func TestSessionKeepsBoundTargetAfterStartupAttachFailure(t *testing.T) {
	b := &recoveryBackend{fakeBackend: fakeBackend{mode: ModeLive, obtained: ObtainedLive}}
	opens := 0
	stubOpen(t, func(context.Context, Mode, string) (Backend, error) {
		opens++
		return b, &cdp.Error{Code: -32001, Message: "Session with given id not found"}
	})
	s, _ := NewManager(ModeLive).Session("bound")
	calls := 0
	_, err := s.Do(t.Context(), func(got Backend) (string, error) {
		calls++
		if got != b {
			t.Fatal("lost backend with bound target")
		}
		return "ready", nil
	})
	if err != nil || opens != 1 || calls != 1 || b.invalidations != 1 {
		t.Fatalf("bound target reopened: err=%v opens=%d calls=%d invalidations=%d", err, opens, calls, b.invalidations)
	}
}

func TestExtensionNamesCannotSilentlySharePinnedTab(t *testing.T) {
	m := NewManager(ModeExtension)
	first, err := m.Session("first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Session("second"); err == nil {
		t.Fatal("two extension sessions silently shared pinned tab")
	}
	if again, err := m.Session("first"); err != nil || again != first {
		t.Fatalf("original extension session unavailable: %v", err)
	}
}

func TestSessionDoesNotRetryPermanentOpenFailure(t *testing.T) {
	for _, cause := range []error{ErrPermissionBlocked, ErrNoLiveBrowser, context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			opens := 0
			stubOpen(t, func(context.Context, Mode, string) (Backend, error) { opens++; return nil, cause })
			s, _ := NewManager(ModeLive).Session("blocked")
			_, err := s.Do(t.Context(), func(Backend) (string, error) { t.Error("program ran"); return "", nil })
			if !errors.Is(err, cause) || opens != 1 {
				t.Fatalf("permanent error retried: err=%v opens=%d", err, opens)
			}
		})
	}
}

func TestSessionInvalidatesOnlyKnownProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cause      error
		invalidate bool
	}{
		{"session missing", &cdp.Error{Code: -32001, Message: "Session with given id not found"}, true},
		{"target missing", &cdp.Error{Code: -32602, Message: "No target with given id found"}, true},
		{"unrelated protocol error", &cdp.Error{Code: -32001, Message: "Unrelated domain failure"}, false},
		{"page exception text", errors.New("JavaScript evaluation failed: Session with given id not found EOF"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &recoveryBackend{}
			stubOpen(t, func(context.Context, Mode, string) (Backend, error) { return b, nil })
			s, _ := NewManager(ModeLive).Session("protocol")
			calls := 0
			_, err := s.Do(t.Context(), func(Backend) (string, error) { calls++; return "", fmt.Errorf("helper: %w", tc.cause) })
			if calls != 1 || !errors.Is(err, tc.cause) || (b.invalidations != 0) != tc.invalidate {
				t.Fatalf("calls=%d err=%v invalidations=%d wantInvalidate=%v", calls, err, b.invalidations, tc.invalidate)
			}
		})
	}
}
