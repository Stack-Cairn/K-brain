package bashrun

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Detached is a command Run handed over while it was still running. Its
// output keeps being drained; the new owner takes it over with Stream and
// reaps the command with Wait. Until Wait returns it stays tracked, so KillAll
// still stops it.
type Detached struct {
	Cmd     *exec.Cmd
	PID     int
	Started time.Time

	r       *pipedRun
	release func()

	waitOnce sync.Once
	waitErr  error
}

func newDetached(r *pipedRun, release func()) *Detached {
	return &Detached{Cmd: r.cmd, PID: r.cmd.Process.Pid, Started: r.started, r: r, release: release}
}

// Output is the bounded tail of what the command printed before Stream, and the
// total number of bytes it printed so far.
func (d *Detached) Output() (string, int64) {
	d.r.mu.Lock()
	defer d.r.mu.Unlock()
	return string(d.r.out.data), d.r.out.total
}

// Stream writes everything the command printed so far to w, then sends its
// further output there as it arrives. Output that could not be kept (more than
// the in-memory tail when no spill file was available) is marked as missing.
func (d *Detached) Stream(w io.Writer) error {
	r := d.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sink != nil {
		return errors.New("detached output is already streamed")
	}
	var err error
	if f := r.out.file; f != nil {
		name := f.Name()
		_ = f.Close()
		if r.out.spillErr == nil {
			err = copyFileTo(w, name)
		} else {
			err = writeTail(w, &r.out)
		}
		_ = os.Remove(name)
	} else {
		err = writeTail(w, &r.out)
	}
	r.out = outputBuffer{}
	r.sink = w
	return err
}

func writeTail(w io.Writer, out *outputBuffer) error {
	if out.total > int64(len(out.data)) {
		if _, err := io.WriteString(w, "[earlier output was not kept]\n"); err != nil {
			return err
		}
	}
	_, err := w.Write(out.data)
	return err
}

func copyFileTo(w io.Writer, name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// Wait waits for the command to exit and its output to be drained, and
// returns what exec.Cmd.Wait returned. It is safe to call more than once.
func (d *Detached) Wait() error {
	d.waitOnce.Do(func() {
		d.waitErr, _ = d.r.finish()
		untrack(d.Cmd)
		d.release()
	})
	return d.waitErr
}

// Kill stops the command and its process group.
func (d *Detached) Kill() { d.r.state.kill() }
