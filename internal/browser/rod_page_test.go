package browser

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

func TestRodPageCallTargetLifetime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"detached", &cdp.Error{Code: -32001, Message: "Session with given id not found"}},
		{"rod-detached", context.Canceled},
		{"closed", ErrTargetClosed},
		{"connection", io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newRecoveryBrowser(t, t.Context())
			life, cancelLife := context.WithCancelCause(b.page.GetContext())
			defer cancelLife(nil)
			b.page = b.page.Context(life)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			calls := 0
			value, err := rodPageCall(ctx, b, func(p *rod.Page) (string, error) {
				calls++
				cancelLife(tc.cause)
				<-p.GetContext().Done()
				return "effect dispatched", p.GetContext().Err()
			})
			if ctx.Err() != nil {
				t.Fatalf("page lifetime did not interrupt before caller deadline: %v", err)
			}
			if tc.cause == context.Canceled {
				if !isSessionErr(err) || errors.Is(err, context.Canceled) {
					t.Fatalf("Rod cancellation must normalize to session loss: %v", err)
				}
			} else if !errors.Is(err, tc.cause) {
				t.Fatalf("lost page cancellation cause: got %v, want %v", err, tc.cause)
			}
			if value != "effect dispatched" || calls != 1 {
				t.Fatalf("partial result lost or operation replayed: value=%q calls=%d", value, calls)
			}
		})
	}
}

func TestRodPageCallCallerCancellationWins(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancel"
		if timeout {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			b, _ := newRecoveryBrowser(t, t.Context())
			life, cancelLife := context.WithCancelCause(b.page.GetContext())
			defer cancelLife(nil)
			b.page = b.page.Context(life)
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			_, err := rodPageCall(ctx, b, func(p *rod.Page) (string, error) {
				if !timeout {
					cancel()
				}
				<-ctx.Done()
				cancelLife(ErrTargetClosed)
				<-p.GetContext().Done()
				return "partial", p.GetContext().Err()
			})
			if !errors.Is(err, want) {
				t.Fatalf("caller cancellation precedence: got %v, want %v", err, want)
			}
		})
	}
}

func TestRodPageCallReleasesCallContext(t *testing.T) {
	b, _ := newRecoveryBrowser(t, t.Context())
	var call context.Context
	wantErr := errors.New("operation failed")
	value, err := rodPageCall(t.Context(), b, func(p *rod.Page) (int, error) {
		call = p.GetContext()
		return 42, wantErr
	})
	if value != 42 || !errors.Is(err, wantErr) {
		t.Fatalf("operation result changed: value=%d error=%v", value, err)
	}
	if call.Err() != context.Canceled {
		t.Fatal("completed call context remains active")
	}
	if b.page.GetContext().Err() != nil || t.Context().Err() != nil {
		t.Fatal("completing a call canceled its page or caller")
	}
}
