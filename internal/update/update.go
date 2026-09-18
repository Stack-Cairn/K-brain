package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const (
	noticeFile = "update.json"

	checkTTL = 24 * time.Hour
)

var latestURL = "https://api.github.com/repos/Stack-Cairn/K-brain/releases/latest"

var fetchTimeout = 2 * time.Second

type Notice struct {
	CheckedAt    time.Time `json:"checkedAt"`
	Latest       string    `json:"latest,omitempty"`
	Acknowledged bool      `json:"acknowledged,omitempty"`
}

var fetchLatest = fetchLatestGitHub

func Check(current string) string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return check(current, filepath.Join(dir, noticeFile), fetchLatest, time.Now())
}

func Pending(current string) string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	n, err := readNotice(filepath.Join(dir, noticeFile))
	if err != nil || n.Acknowledged || !Newer(current, n.Latest) {
		return ""
	}
	return n.Latest
}

func Acknowledge() {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	p := filepath.Join(dir, noticeFile)
	n, err := readNotice(p)
	if err != nil || n.Latest == "" {
		return
	}
	n.Acknowledged = true
	_ = writeNotice(p, n)
}

func check(current, noticePath string, fetch func() (string, error), now time.Time) string {
	if current == "dev" || current == "" {
		return ""
	}
	n, err := readNotice(noticePath)
	if err == nil {
		if n.Latest != "" && !n.Acknowledged {
			if Newer(current, n.Latest) {
				return ""
			}

			n.Latest = ""
		}
		if now.Sub(n.CheckedAt) < checkTTL {
			return ""
		}
	}
	latest, err := fetch()
	if err != nil || !Newer(current, latest) {

		_ = writeNotice(noticePath, Notice{CheckedAt: now})
		return ""
	}
	_ = writeNotice(noticePath, Notice{CheckedAt: now, Latest: latest})
	return latest
}

func Newer(current, latest string) bool {
	c, cpre, lok := parseSemver(current)
	l, lpre, rok := parseSemver(latest)
	if !lok || !rok {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return cpre && !lpre
}

func parseSemver(v string) (nums [3]int, prerelease, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	base, suffix, _ := strings.Cut(v, "-")
	parts := strings.SplitN(base, ".", 3)
	if len(parts) != 3 {
		return nums, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, false, false
		}
		nums[i] = n
	}
	return nums, suffix != "", true
}

func readNotice(path string) (Notice, error) {
	var n Notice
	data, err := os.ReadFile(path)
	if err != nil {
		return n, err
	}
	if err := json.Unmarshal(data, &n); err != nil {
		return Notice{}, err
	}
	return n, nil
}

func writeNotice(path string, n Notice) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func fetchLatestGitHub() (string, error) {

	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := ghToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", errors.New("github: empty tag_name")
	}
	return rel.TagName, nil
}

func ghToken() string {
	if out, err := exec.CommandContext(context.Background(), "gh", "auth", "token").Output(); err == nil {
		if tok := strings.TrimSpace(string(out)); tok != "" {
			return tok
		}
	}
	return os.Getenv("GH_TOKEN")
}
