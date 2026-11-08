package backend

import (
	"path/filepath"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

// runSandboxVersion is the contract advertised as capabilities.run_sandbox.
const runSandboxVersion = "kbrain.sandbox.v1"

// sandboxForRun is replaced in tests that run without a platform sandbox.
var sandboxForRun = sandbox.ForRun

// runSandboxPolicy returns the sandbox policy for a run's commands. Without
// options.sandbox the configured policy applies unchanged. With it, the run's
// write roots become the grants (or the configured policy is tightened), and an
// error is returned when the machine cannot enforce it, so the run is refused
// rather than started unconfined.
func (s *Server) runSandboxPolicy(configured *sandbox.Policy, cwd string, options protocol.RunOptions) (*sandbox.Policy, error) {
	if options.Sandbox == nil {
		return configured, nil
	}
	var writable []string
	for _, root := range options.WorkspaceRoots {
		if root.Access == "write" && filepath.Clean(root.Path) != filepath.Clean(cwd) {
			writable = append(writable, root.Path)
		}
	}
	return sandboxForRun(configured, sandbox.RunRequest{
		Root:     cwd,
		Writable: writable,
		Network:  options.Sandbox.Network,
		Masked:   s.sandboxMasked,
		EnvKeep:  s.sandboxEnvKeep,
	})
}

// sandboxProbeWait bounds how long /v1/health waits for the sandbox probe.
// The probe launches a confined process; on a machine where that launch
// hangs, health must still answer or clients take the backend for dead.
var sandboxProbeWait = 2 * time.Second

// runSandboxCapability is the /v1/health description of per-run sandboxing.
func runSandboxCapability() healthRunSandboxV1 {
	c, ok := sandbox.ProbeWithin(sandboxProbeWait)
	if !ok {
		return healthRunSandboxV1{
			Version: runSandboxVersion,
			Modes:   []string{"workspace"},
			Reason:  "the sandbox capability check has not finished yet",
		}
	}
	return healthRunSandboxV1{
		Supported:      c.Supported,
		Version:        runSandboxVersion,
		Mechanism:      c.Mechanism,
		Platform:       c.Platform,
		NetworkControl: c.NetworkControl,
		ReadMasking:    c.ReadMasking,
		PathProtection: c.PathProtection,
		Modes:          []string{"workspace"},
		Reason:         c.Reason,
		SetupAvailable: c.SetupAvailable,
		SetupHint:      c.SetupHint,
	}
}
