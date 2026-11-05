package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
)

// The sandbox-user setup is the elevated level of the native Windows sandbox.
// An administrator runs it once (`kn sandbox setup`); it creates two
// hidden local accounts for the user who ran it, an online one and an offline
// one, two local groups and Windows Filtering Platform filters that block
// every connection of the offline account. Commands then run as one of those
// accounts instead of as the user (windows_user.go). This file holds the parts
// that do not depend on Windows, so that they are tested everywhere.

// MechanismSandboxUser is the capability mechanism of commands run as a
// dedicated sandbox account.
const MechanismSandboxUser = "sandbox-user"

var (
	// ErrSetupUnsupported is returned by Setup and Uninstall off Windows.
	ErrSetupUnsupported = errors.New("sandbox setup is only available on Windows")
	// ErrSetupDeclined is returned when the administrator approval prompt was
	// declined or dismissed.
	ErrSetupDeclined = errors.New("administrator approval was declined; nothing was changed")
)

// setupHint is the capability's setup_hint while the setup is not in place.
const setupHint = "Run `kn sandbox setup` (administrator approval required) to block network access in offline runs and keep sandboxed commands out of other workspaces."

// repairHint is the setup_hint when the setup exists but does not work.
func repairHint(problem string) string {
	return "The sandbox user setup is not working (" + problem + "); run `kn sandbox setup` again to repair it."
}

// SetupStatus describes the sandbox-user setup of the current user.
type SetupStatus struct {
	Platform string `json:"platform"`
	// Available is true where the setup can be run (Windows).
	Available bool `json:"available"`
	// Installed is true when the setup's state exists for this user.
	Installed bool `json:"installed"`
	// Active is true when commands run as the sandbox accounts.
	Active    bool   `json:"active"`
	Mechanism string `json:"mechanism"`
	// NetworkControl is true when offline runs are blocked from the network.
	NetworkControl bool     `json:"network_control"`
	Accounts       []string `json:"accounts,omitempty"`
	Groups         []string `json:"groups,omitempty"`
	StatePath      string   `json:"state_path,omitempty"`
	Problem        string   `json:"problem,omitempty"`
	Hint           string   `json:"hint,omitempty"`
}

// Setup creates or repairs the sandbox-user setup of the current user. It asks
// for administrator approval (UAC) unless the process is already elevated,
// and never elevates without asking. The sandbox capability is probed again
// afterwards.
func Setup(ctx context.Context) (SetupStatus, error) {
	if runtime.GOOS != "windows" {
		return SetupStatusNow(), ErrSetupUnsupported
	}
	err := setupPlatform(ctx)
	Reprobe()
	return SetupStatusNow(), err
}

// Uninstall removes the current user's sandbox accounts, groups, filters,
// stored password and the access entries the sandbox added. It is idempotent.
func Uninstall(ctx context.Context) (SetupStatus, error) {
	if runtime.GOOS != "windows" {
		return SetupStatusNow(), ErrSetupUnsupported
	}
	err := uninstallPlatform(ctx)
	Reprobe()
	return SetupStatusNow(), err
}

// SetupStatusNow reports the current user's setup without changing anything.
func SetupStatusNow() SetupStatus {
	c := Probe()
	s := SetupStatus{Platform: runtime.GOOS, Mechanism: c.Mechanism, Active: c.Mechanism == MechanismSandboxUser, NetworkControl: c.NetworkControl, Hint: c.SetupHint}
	if runtime.GOOS != "windows" {
		return s
	}
	s.Available = true
	statusPlatform(&s)
	return s
}

// setupNames are the account, group and filter names of one user's setup.
// They derive from the user's SID, so every user of the machine gets their own
// accounts and nobody's setup or uninstall touches another's.
type setupNames struct {
	// key is a short hex digest of the user's SID (and the test namespace).
	key            string
	online         string
	offline        string
	group          string
	offlineGroup   string
	sidAuthorities [2]uint32
}

// setupNamespace separates the accounts of the administrator tests from a real
// setup on the same machine.
var setupNamespace = ""

// Synthetic SIDs live under the resource manager authority (S-1-9), which no
// account uses. sidMagic marks them as this sandbox's.
const sidMagic = 0x4b627261

func namesFor(userSID string) setupNames {
	sum := sha256.Sum256([]byte(setupNamespace + "\x00" + strings.ToUpper(userSID)))
	key := hex.EncodeToString(sum[:3])
	prefix := "KBrainSbx"
	if setupNamespace != "" {
		prefix = "KBrainTst"
	}
	// Local account names are at most 20 characters: 9 + 1 + 6 + 4.
	return setupNames{
		key:            key,
		online:         prefix + "-" + key + "-On",
		offline:        prefix + "-" + key + "-Off",
		group:          prefix + "-" + key,
		offlineGroup:   prefix + "-" + key + "-Offline",
		sidAuthorities: [2]uint32{sidMagic, binary.BigEndian.Uint32(sum[3:7])},
	}
}

// sidPrefix is the start of every synthetic SID of this user's setup.
func (n setupNames) sidPrefix() string {
	return fmt.Sprintf("S-1-9-%d-%d-", n.sidAuthorities[0], n.sidAuthorities[1])
}

// readerSID is granted read access to the paths every sandboxed command needs
// outside its workspace (the launcher binary, PATH directories in the profile).
func (n setupNames) readerSID() string { return n.sidPrefix() + "0" }

// workspaceSID is the per-workspace capability: the identity that is granted
// write access to one writable root, and is carried only by the commands of
// runs that write that root.
func (n setupNames) workspaceSID(root string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return fmt.Sprintf("%s%d-%d-%d", n.sidPrefix(), binary.BigEndian.Uint32(sum[0:4]), binary.BigEndian.Uint32(sum[4:8]), binary.BigEndian.Uint32(sum[8:12]))
}

// guid derives a stable GUID (version 5 layout) for one of the setup's
// filtering objects.
func (n setupNames) guid(label string) [16]byte {
	sum := sha256.Sum256([]byte("k-brain sandbox wfp\x00" + n.key + "\x00" + label))
	var g [16]byte
	copy(g[:], sum[:16])
	g[6] = g[6]&0x0f | 0x50
	g[8] = g[8]&0x3f | 0x80
	return g
}

// setupStateFile is where the setup's state (account names and SIDs, and the
// accounts' passwords encrypted with DPAPI) is kept: in the user's data
// directory, which is masked from sandboxed commands, under a file of its own
// that only the user, administrators and SYSTEM can open.
const setupStateFile = "windows-sandbox-user.json"

// aclRootsFile lists every path the sandbox added access entries to, so that
// uninstall can remove them.
const aclRootsFile = "windows-sandbox-acl-roots.txt"

// setupStateDir is the "sandbox" directory of the user's data directory,
// resolved without creating or migrating anything.
func setupStateDir() string {
	for _, env := range []string{datapath.LiveAgentHomeEnv, datapath.LegacyHomeEnv} {
		if dir := strings.TrimSpace(os.Getenv(env)); dir != "" {
			return filepath.Join(dir, "sandbox")
		}
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, datapath.LiveAgentDirName, "sandbox")
}

// offlineUnavailable is the reason an offline run is refused without the
// setup.
const offlineUnavailable = "the native Windows sandbox blocks network access only after `kn sandbox setup` (administrator approval required); run it, allow network for the sandbox, or run K-brain inside WSL2"
