package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// runtimeSockets are the Unix sockets through which a command could start
// processes outside the sandbox: container engines (a container can mount the
// host), the D-Bus buses and the systemd managers (transient units run
// unconfined). A filesystem path socket is reachable even without network
// access, so these are hidden in every mode where the mechanism can.
func runtimeSockets(goos string) []string {
	var paths []string
	home, _ := os.UserHomeDir()
	if home != "" {
		paths = append(paths, filepath.Join(home, ".docker", "run", "docker.sock"), filepath.Join(home, ".docker", "desktop", "docker.sock"))
	}
	if host, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && filepath.IsAbs(host) {
		paths = append(paths, host)
	}
	switch goos {
	case "darwin":
		paths = append(paths, "/var/run/docker.sock")
	case "linux":
		for _, run := range []string{"/run", "/var/run"} {
			paths = append(paths,
				run+"/docker.sock",
				run+"/podman/podman.sock",
				run+"/containerd/containerd.sock",
				run+"/dbus/system_bus_socket",
				run+"/systemd/private",
			)
		}
		var userDirs []string
		if uid := os.Getuid(); uid >= 0 {
			userDirs = append(userDirs, fmt.Sprintf("/run/user/%d", uid))
		}
		if xdg := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(xdg) {
			userDirs = append(userDirs, xdg)
		}
		for _, dir := range userDirs {
			paths = append(paths,
				filepath.Join(dir, "docker.sock"),
				filepath.Join(dir, "podman", "podman.sock"),
				filepath.Join(dir, "bus"),
				filepath.Join(dir, "systemd", "private"),
			)
		}
	}
	return paths
}

// existingSockets resolves the candidates that exist as sockets now.
func existingSockets(candidates []string) []string {
	var out []string
	for _, path := range candidates {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || info.Mode()&fs.ModeSocket == 0 {
			continue
		}
		out = appendNew(out, resolved)
	}
	return out
}

// wslInteropDir holds the sockets WSL uses to start Windows programs from
// Linux; a Windows process is outside every Linux sandbox.
const wslInteropDir = "/run/WSL"
