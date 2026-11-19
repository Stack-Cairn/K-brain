package sshmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

// SFTP text limits. Default tool calls remain bounded pages; full reads are explicit.
const (
	DefaultReadTextBytes = 200 << 10
	MinReadTextBytes     = 4 << 10
	MaxReadTextBytes     = 256 << 10
	MaxFullTextBytes     = 16 << 20
	sftpStartTimeout     = 30 * time.Second
	// maxTransferFiles bounds one recursive transfer.
	maxTransferFiles = 100000
)

// SFTPEntry is one remote file. Field names follow LiveAgent v1.
type SFTPEntry struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"sizeBytes"`
	// Mtime is in milliseconds (second precision).
	Mtime int64 `json:"mtime"`
}

// SFTPList is an sftp_list result.
type SFTPList struct {
	Path    string      `json:"path"`
	Entries []SFTPEntry `json:"entries"`
}

// SFTPStat is an sftp_stat result.
type SFTPStat struct {
	Exists bool       `json:"exists"`
	Entry  *SFTPEntry `json:"entry,omitempty"`
}

// SFTPReadText is an sftp_read_text result.
type SFTPReadText struct {
	Path      string     `json:"path"`
	Content   string     `json:"content"`
	Offset    int64      `json:"offset"`
	BytesRead int        `json:"bytesRead"`
	SizeBytes int64      `json:"sizeBytes"`
	Truncated bool       `json:"truncated"`
	Entry     *SFTPEntry `json:"entry,omitempty"`
	// ContentHash is SHA-256 of the complete bytes, present only for full reads.
	ContentHash string `json:"contentHash,omitempty"`
}

// SFTPWriteOptions optionally checks the version returned by a full read.
// SFTP has no compare-and-swap: external writers can still race this check,
// and writes truncate in place, so a failed write can leave partial content.
type SFTPWriteOptions struct {
	ExpectedMtime       *int64
	ExpectedContentHash *string
}

// SFTPAction is the result of a write, mkdir, rename or delete.
type SFTPAction struct {
	Action   string         `json:"action"`
	Path     string         `json:"path"`
	Entry    *SFTPEntry     `json:"entry,omitempty"`
	Transfer *TransferState `json:"transfer,omitempty"`
}

// TransferState is an upload or download as sftp_transfer_status reports it.
type TransferState struct {
	ID          string `json:"id"`
	SessionID   string `json:"sessionId"`
	Direction   string `json:"direction"`
	Status      string `json:"status"`
	SourcePath  string `json:"sourcePath"`
	TargetPath  string `json:"targetPath"`
	CurrentPath string `json:"currentPath"`
	BytesDone   int64  `json:"bytesDone"`
	BytesTotal  int64  `json:"bytesTotal"`
	FilesDone   int    `json:"filesDone"`
	FilesTotal  int    `json:"filesTotal"`
	Error       string `json:"error,omitempty"`
}

// TransferResponse wraps a TransferState like LiveAgent v1.
type TransferResponse struct {
	Transfer TransferState `json:"transfer"`
}

// Transfer statuses.
const (
	TransferQueued    = "queued"
	TransferRunning   = "running"
	TransferCompleted = "completed"
	TransferFailed    = "failed"
	TransferCancelled = "cancelled"
)

// transfer is an upload or download running in the background.
type transfer struct {
	id    string
	mu    sync.Mutex
	state TransferState
	stop  context.CancelFunc
	done  chan struct{}
}

func (t *transfer) cancel() { t.stop() }

func (t *transfer) snapshot() TransferState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

func (t *transfer) update(fn func(*TransferState)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(&t.state)
}

func (t *transfer) finished() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// EnableSFTP starts the session's SFTP client when it has none.
func (s *Session) EnableSFTP(ctx context.Context) error {
	done, err := s.use()
	if err != nil {
		return err
	}
	defer done()
	_, err = s.sftpClient(ctx)
	return err
}

// sftpClient returns the session's SFTP client, starting it on first use.
func (s *Session) sftpClient(ctx context.Context) (*sftp.Client, error) {
	s.sftpInit.Lock()
	defer s.sftpInit.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, s.closedError()
	}
	if s.sftp != nil {
		c := s.sftp
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	ch := make(chan sftpStart, 1)
	go func() {
		c, err := sftp.NewClient(s.client)
		ch <- sftpStart{c, err}
	}()
	var r sftpStart
	timer := time.NewTimer(sftpStartTimeout)
	defer timer.Stop()
	select {
	case r = <-ch:
	case <-ctx.Done():
		go discardSFTP(ch)
		return nil, ctx.Err()
	case <-timer.C:
		go discardSFTP(ch)
		return nil, errors.New("the SSH server did not start SFTP in time")
	}
	if r.err != nil {
		return nil, fmt.Errorf("the SSH server refused SFTP: %v", r.err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		r.c.Close()
		return nil, s.closedError()
	}
	s.sftp = r.c
	s.mu.Unlock()
	go func(c *sftp.Client) {
		// The next call starts a new client when the server ends this one.
		_ = c.Wait()
		s.mu.Lock()
		if s.sftp == c {
			s.sftp = nil
		}
		s.mu.Unlock()
	}(r.c)
	return r.c, nil
}

type sftpStart struct {
	c   *sftp.Client
	err error
}

// discardSFTP closes a client that started after its caller gave up.
func discardSFTP(ch <-chan sftpStart) {
	if r := <-ch; r.c != nil {
		r.c.Close()
	}
}

// withSFTP runs fn with the session marked busy and its SFTP client started.
func withSFTP[T any](ctx context.Context, s *Session, fn func(*sftp.Client) (T, error)) (T, error) {
	var zero T
	done, err := s.use()
	if err != nil {
		return zero, err
	}
	defer done()
	c, err := s.sftpClient(ctx)
	if err != nil {
		return zero, err
	}
	return fn(c)
}

// NormalizeRemotePath is LiveAgent v1's normalize_remote_path: backslashes
// become slashes, "." and ".." parts are dropped, a leading "/" is kept and
// the empty path is ".".
func NormalizeRemotePath(p string) string {
	raw := strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if raw == "" || raw == "." {
		return "."
	}
	absolute := strings.HasPrefix(raw, "/")
	var parts []string
	for _, part := range strings.Split(raw, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." {
			continue
		}
		parts = append(parts, part)
	}
	switch {
	case absolute:
		return "/" + strings.Join(parts, "/")
	case len(parts) == 0:
		return "."
	}
	return strings.Join(parts, "/")
}

func joinRemote(parent, child string) string {
	child = strings.Trim(strings.TrimSpace(child), "/")
	parent = NormalizeRemotePath(parent)
	switch {
	case child == "":
		return parent
	case parent == "/":
		return "/" + child
	case parent == ".":
		return child
	}
	return strings.TrimRight(parent, "/") + "/" + child
}

func remoteParent(p string) (string, bool) {
	p = NormalizeRemotePath(p)
	if p == "." || p == "/" {
		return "", false
	}
	i := strings.LastIndex(p, "/")
	switch {
	case i == 0:
		return "/", true
	case i > 0:
		return p[:i], true
	}
	return ".", true
}

func remoteBase(p string) string {
	p = NormalizeRemotePath(p)
	if p == "." || p == "/" {
		return ""
	}
	return path.Base(p)
}

// safeName reports a directory entry name that stays inside its directory.
func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && !strings.ContainsRune(name, 0)
}

func entryKind(info os.FileInfo) string {
	switch {
	case info.IsDir():
		return "directory"
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	}
	return "file"
}

func entryOf(p, name string, info os.FileInfo) SFTPEntry {
	if name == "" {
		name = p
	}
	return SFTPEntry{Path: p, Name: name, Kind: entryKind(info), SizeBytes: info.Size(), Mtime: info.ModTime().Unix() * 1000}
}

func statEntry(c *sftp.Client, p string) (*SFTPEntry, error) {
	p = NormalizeRemotePath(p)
	info, err := c.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("remote stat failed: %v", err)
	}
	e := entryOf(p, remoteBase(p), info)
	return &e, nil
}

func isNotExist(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var status *sftp.StatusError
	return errors.As(err, &status) && status.FxCode() == sftp.ErrSSHFxNoSuchFile
}

func remoteExists(c *sftp.Client, p string) (bool, error) {
	_, err := c.Lstat(p)
	switch {
	case err == nil:
		return true, nil
	case isNotExist(err):
		return false, nil
	}
	return false, fmt.Errorf("remote stat failed: %v", err)
}

func sortEntries(entries []SFTPEntry) {
	slices.SortFunc(entries, func(a, b SFTPEntry) int {
		ad, bd := a.Kind == "directory", b.Kind == "directory"
		if ad != bd {
			if ad {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// List lists a remote directory ("." is the login directory).
func (s *Session) List(ctx context.Context, p string) (SFTPList, error) {
	return withSFTP(ctx, s, func(c *sftp.Client) (SFTPList, error) {
		p := NormalizeRemotePath(p)
		listPath := p
		if real, err := c.RealPath(p); err == nil && real != "" {
			listPath = NormalizeRemotePath(real)
		}
		infos, err := c.ReadDirContext(ctx, listPath)
		if err != nil {
			return SFTPList{}, fmt.Errorf("remote list failed: %v", err)
		}
		entries := make([]SFTPEntry, 0, len(infos))
		for _, info := range infos {
			name := info.Name()
			if name == "." || name == ".." {
				continue
			}
			entries = append(entries, entryOf(joinRemote(listPath, name), name, info))
		}
		sortEntries(entries)
		return SFTPList{Path: listPath, Entries: entries}, nil
	})
}

// Stat describes a remote path; a missing path is not an error.
func (s *Session) Stat(ctx context.Context, p string) (SFTPStat, error) {
	return withSFTP(ctx, s, func(c *sftp.Client) (SFTPStat, error) {
		p := NormalizeRemotePath(p)
		info, err := c.Stat(p)
		switch {
		case isNotExist(err):
			return SFTPStat{Exists: false}, nil
		case err != nil:
			return SFTPStat{}, fmt.Errorf("remote stat failed: %v", err)
		}
		e := entryOf(p, remoteBase(p), info)
		return SFTPStat{Exists: true, Entry: &e}, nil
	})
}

// ReadText reads a UTF-8 page, aligning offset forward to a rune boundary.
// maxBytes is capped at 256 KiB. The default SSHManager call remains a page.
func (s *Session) ReadText(ctx context.Context, p string, offset int64, maxBytes int) (SFTPReadText, error) {
	if offset < 0 || offset > 1<<63-1-int64(MaxReadTextBytes)-utf8.UTFMax {
		return SFTPReadText{}, errors.New("remote text offset is out of range")
	}
	return s.readText(ctx, p, offset, max(1, min(maxBytes, MaxReadTextBytes)), false)
}

// ReadFullText reads the entire file or fails, without lossy UTF-8 conversion.
func (s *Session) ReadFullText(ctx context.Context, p string) (SFTPReadText, error) {
	return s.readText(ctx, p, 0, MaxFullTextBytes, true)
}

func (s *Session) readText(ctx context.Context, p string, offset int64, maxBytes int, full bool) (SFTPReadText, error) {
	return withSFTP(ctx, s, func(c *sftp.Client) (SFTPReadText, error) {
		p = NormalizeRemotePath(p)
		// Preserve directory/stat errors before opening; use handle metadata for
		// the actual read so replacing the path does not switch its identity.
		entry, err := c.Stat(p)
		if err != nil {
			return SFTPReadText{}, fmt.Errorf("remote stat failed: %v", err)
		}
		if entry.IsDir() {
			return SFTPReadText{}, errors.New("remote path is a directory")
		}
		f, err := c.Open(p)
		if err != nil {
			return SFTPReadText{}, fmt.Errorf("failed to open remote file: %v", err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return SFTPReadText{}, fmt.Errorf("remote stat failed: %v", err)
		}
		if info.IsDir() {
			return SFTPReadText{}, errors.New("remote path is a directory")
		}
		if !info.Mode().IsRegular() {
			return SFTPReadText{}, errors.New("remote path is not a regular file")
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return SFTPReadText{}, fmt.Errorf("failed to seek remote file: %v", err)
		}
		var buf []byte
		var more bool
		if full {
			buf, err = readFullUTF8(contextReader{ctx, f})
		} else {
			buf, offset, more, err = readUTF8Page(contextReader{ctx, f}, offset, maxBytes)
		}
		if err != nil {
			return SFTPReadText{}, err
		}
		after, err := f.Stat()
		if err != nil {
			return SFTPReadText{}, fmt.Errorf("remote stat failed: %v", err)
		}
		if full && (info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime())) {
			return SFTPReadText{}, errors.New("remote file changed while reading; reload before saving")
		}
		e := entryOf(p, remoteBase(p), after)
		size := after.Size()
		if full {
			size = int64(len(buf))
		} else if len(buf) > 0 || more {
			observed := offset + int64(len(buf))
			if more {
				observed++
			}
			size = max(size, observed)
		}
		e.SizeBytes = size
		result := SFTPReadText{Path: p, Content: string(buf), Offset: offset, BytesRead: len(buf), SizeBytes: size, Truncated: !full && (more || offset+int64(len(buf)) < size), Entry: &e}
		if full {
			hash := sha256.Sum256(buf)
			result.ContentHash = hex.EncodeToString(hash[:])
		}
		return result, nil
	})
}

// readFullUTF8 bounds actual I/O independently of remote size metadata.
func readFullUTF8(r io.Reader) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxFullTextBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read remote file: %v", err)
	}
	if len(buf) > MaxFullTextBytes {
		return nil, errors.New("remote text exceeds 16 MiB")
	}
	if !utf8.Valid(buf) {
		return nil, errors.New("remote text is not valid UTF-8")
	}
	return buf, nil
}

func readUTF8Page(r io.Reader, offset int64, maxBytes int) ([]byte, int64, bool, error) {
	maxBytes = max(1, min(maxBytes, MaxReadTextBytes))
	// Three possible leading continuation bytes plus one byte of lookahead.
	buf, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+utf8.UTFMax))
	if err != nil {
		return nil, offset, false, fmt.Errorf("failed to read remote file: %v", err)
	}
	start := 0
	if offset > 0 {
		for start < len(buf) && start < utf8.UTFMax-1 && buf[start]&0xc0 == 0x80 {
			start++
		}
	}
	offset += int64(start)
	buf = buf[start:]
	end := min(len(buf), maxBytes)
	// Keep complete runes only; malformed interior bytes are errors.
	validEnd := 0
	for validEnd < end {
		if !utf8.FullRune(buf[validEnd:end]) {
			break
		}
		_, n := utf8.DecodeRune(buf[validEnd:end])
		if n == 1 && buf[validEnd] >= utf8.RuneSelf {
			return nil, offset, false, errors.New("remote text is not valid UTF-8")
		}
		validEnd += n
	}
	if validEnd < end && len(buf) <= end {
		return nil, offset, false, errors.New("remote text ends with incomplete UTF-8")
	}
	return buf[:validEnd], offset, validEnd < len(buf), nil
}

// WriteText preserves the existing overwrite API, with a 16 MiB UTF-8 cap.
func (s *Session) WriteText(ctx context.Context, p, content string, overwrite, createParents bool) (SFTPAction, error) {
	return s.WriteTextWithOptions(ctx, p, content, overwrite, createParents, SFTPWriteOptions{})
}

// WriteTextWithOptions checks an optional full-read version before truncating.
// This is best-effort conflict detection, not atomic remote replacement.
func (s *Session) WriteTextWithOptions(ctx context.Context, p, content string, overwrite, createParents bool, opts SFTPWriteOptions) (SFTPAction, error) {
	if err := validateTextWrite(content, overwrite, opts); err != nil {
		return SFTPAction{}, err
	}
	return withSFTP(ctx, s, func(c *sftp.Client) (SFTPAction, error) {
		p = NormalizeRemotePath(p)
		if !overwrite {
			exists, err := remoteExists(c, p)
			if err != nil {
				return SFTPAction{}, err
			}
			if exists {
				return SFTPAction{}, fmt.Errorf("target already exists: %s", p)
			}
		}
		if parent, ok := remoteParent(p); ok && createParents {
			if err := ensureRemoteDirs(c, parent); err != nil {
				return SFTPAction{}, err
			}
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if !overwrite {
			// The server, rather than the precheck, arbitrates concurrent creators.
			flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
		} else if opts.ExpectedContentHash != nil {
			// Do not create/truncate until the same open handle passes its check.
			flags = os.O_RDWR
		}
		f, err := c.OpenFile(p, flags)
		if err != nil {
			if opts.ExpectedContentHash != nil && isNotExist(err) {
				return SFTPAction{}, errors.New("remote file changed since read: target was removed")
			}
			return SFTPAction{}, fmt.Errorf("failed to create remote file: %v", err)
		}
		defer f.Close()
		if opts.ExpectedContentHash != nil {
			if err := checkRemoteTextVersion(ctx, f, opts); err != nil {
				return SFTPAction{}, err
			}
			if err := ctx.Err(); err != nil {
				return SFTPAction{}, err
			}
			if err := f.Truncate(0); err != nil {
				return SFTPAction{}, fmt.Errorf("failed to truncate remote file: %v", err)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return SFTPAction{}, err
			}
		}
		if _, err := io.Copy(f, contextReader{ctx, strings.NewReader(content)}); err != nil {
			return SFTPAction{}, fmt.Errorf("failed to write remote file: %v", err)
		}
		if err := f.Close(); err != nil {
			return SFTPAction{}, fmt.Errorf("failed to close remote file: %v", err)
		}
		e, err := statEntry(c, p)
		if err != nil {
			return SFTPAction{}, err
		}
		return SFTPAction{Action: "write_text", Path: p, Entry: e}, nil
	})
}

func validateTextWrite(content string, overwrite bool, opts SFTPWriteOptions) error {
	if len(content) > MaxFullTextBytes || !utf8.ValidString(content) {
		return errors.New("remote write requires valid UTF-8 text within 16 MiB")
	}
	if (opts.ExpectedMtime == nil) != (opts.ExpectedContentHash == nil) {
		return errors.New("expected modification time and content hash must be supplied together")
	}
	if opts.ExpectedContentHash != nil {
		hash, err := hex.DecodeString(*opts.ExpectedContentHash)
		if err != nil || len(hash) != sha256.Size || *opts.ExpectedMtime < 0 {
			return errors.New("invalid expected remote file version")
		}
		if !overwrite {
			return errors.New("expected remote file version requires overwrite")
		}
	}
	return nil
}

func checkRemoteTextVersion(ctx context.Context, f *sftp.File, opts SFTPWriteOptions) error {
	conflict := errors.New("remote file changed since read; reload before saving")
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.ModTime().Unix()*1000 != *opts.ExpectedMtime {
		return conflict
	}
	buf, err := readFullUTF8(contextReader{ctx, f})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(buf)
	if !strings.EqualFold(hex.EncodeToString(hash[:]), *opts.ExpectedContentHash) {
		return conflict
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return conflict
	}
	return nil
}

// Mkdir creates one remote directory.
func (s *Session) Mkdir(ctx context.Context, p string) (SFTPAction, error) {
	return withSFTP(ctx, s, func(c *sftp.Client) (SFTPAction, error) {
		p := NormalizeRemotePath(p)
		if err := c.Mkdir(p); err != nil {
			return SFTPAction{}, fmt.Errorf("remote mkdir failed: %v", err)
		}
		e, err := statEntry(c, p)
		if err != nil {
			return SFTPAction{}, err
		}
		return SFTPAction{Action: "mkdir", Path: p, Entry: e}, nil
	})
}

// Rename renames a remote path.
func (s *Session) Rename(ctx context.Context, from, to string) (SFTPAction, error) {
	return withSFTP(ctx, s, func(c *sftp.Client) (SFTPAction, error) {
		from, to := NormalizeRemotePath(from), NormalizeRemotePath(to)
		if err := c.Rename(from, to); err != nil {
			return SFTPAction{}, fmt.Errorf("remote rename failed: %v", err)
		}
		e, err := statEntry(c, to)
		if err != nil {
			return SFTPAction{}, err
		}
		return SFTPAction{Action: "rename", Path: to, Entry: e}, nil
	})
}

// Delete removes a remote file, symlink or (with recursive) directory. A
// symlink is removed itself; its target is never followed.
func (s *Session) Delete(ctx context.Context, p string, recursive bool) (SFTPAction, error) {
	return withSFTP(ctx, s, func(c *sftp.Client) (SFTPAction, error) {
		p := NormalizeRemotePath(p)
		info, err := c.Lstat(p)
		if err != nil {
			return SFTPAction{}, fmt.Errorf("remote stat failed: %v", err)
		}
		if !info.IsDir() {
			if err := c.Remove(p); err != nil {
				return SFTPAction{}, fmt.Errorf("remote delete failed: %v", err)
			}
			return SFTPAction{Action: "delete", Path: p}, nil
		}
		if !recursive {
			return SFTPAction{}, errors.New("recursive is required to delete a remote directory")
		}
		dirs := []string{p}
		var files []string
		for i := 0; i < len(dirs); i++ {
			if err := ctx.Err(); err != nil {
				return SFTPAction{}, err
			}
			infos, err := c.ReadDirContext(ctx, dirs[i])
			if err != nil {
				return SFTPAction{}, fmt.Errorf("remote list failed: %v", err)
			}
			for _, info := range infos {
				if !safeName(info.Name()) {
					continue
				}
				child := joinRemote(dirs[i], info.Name())
				if info.IsDir() {
					dirs = append(dirs, child)
				} else {
					files = append(files, child)
				}
			}
		}
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return SFTPAction{}, err
			}
			if err := c.Remove(f); err != nil {
				return SFTPAction{}, fmt.Errorf("remote delete failed: %v", err)
			}
		}
		for _, d := range slices.Backward(dirs) {
			if err := c.RemoveDirectory(d); err != nil {
				return SFTPAction{}, fmt.Errorf("remote rmdir failed: %v", err)
			}
		}
		return SFTPAction{Action: "delete", Path: p}, nil
	})
}

func ensureRemoteDirs(c *sftp.Client, p string) error {
	p = NormalizeRemotePath(p)
	if p == "." || p == "/" {
		return nil
	}
	current := "."
	if strings.HasPrefix(p, "/") {
		current = "/"
	}
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		current = joinRemote(current, part)
		if err := c.Mkdir(current); err != nil {
			info, statErr := c.Stat(current)
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("remote mkdir failed: %v", err)
			}
		}
	}
	return nil
}

// contextReader stops reading once ctx is done.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// progressWriter counts bytes into a transfer and stops once ctx is done.
type progressWriter struct {
	ctx context.Context
	w   io.Writer
	t   *transfer
}

func (w progressWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.w.Write(p)
	w.t.update(func(st *TransferState) { st.BytesDone += int64(n) })
	return n, err
}

// progressReader counts bytes into a transfer and stops once ctx is done.
type progressReader struct {
	ctx context.Context
	r   io.Reader
	t   *transfer
}

func (r progressReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	r.t.update(func(st *TransferState) { st.BytesDone += int64(n) })
	return n, err
}

// TransferRequest starts an upload or download.
type TransferRequest struct {
	// Upload: Local is the source, Remote the target directory. Download:
	// Remote is the source, Local the target directory.
	Upload bool
	// Local is an absolute local path already checked against the workspace.
	Local string
	// Display renders a local absolute path for the result.
	Display   func(string) string
	Remote    string
	Recursive bool
	Overwrite bool
}

type localFile struct {
	abs  string
	rel  string
	size int64
}

type remoteFile struct {
	path string
	rel  string
	size int64
}

// StartTransfer validates req and runs it in the background. The returned
// state is "queued"; sftp_transfer_status follows it.
func (s *Session) StartTransfer(ctx context.Context, req TransferRequest) (TransferState, error) {
	if req.Display == nil {
		req.Display = filepath.ToSlash
	}
	release, err := s.use()
	if err != nil {
		return TransferState{}, err
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()
	c, err := s.sftpClient(ctx)
	if err != nil {
		return TransferState{}, err
	}
	t := &transfer{id: newID("sftp"), done: make(chan struct{})}
	t.state = TransferState{ID: t.id, SessionID: s.ID, Status: TransferQueued}
	var run func(context.Context) error
	if req.Upload {
		run, err = s.planUpload(c, t, req)
	} else {
		run, err = s.planDownload(ctx, c, t, req)
	}
	if err != nil {
		return TransferState{}, err
	}
	runCtx, stop := context.WithCancel(context.Background())
	t.stop = stop
	if err := s.addTransfer(t); err != nil {
		stop()
		return TransferState{}, err
	}
	initial := t.snapshot()
	started = true
	go func() {
		defer release()
		defer close(t.done)
		defer stop()
		t.update(func(st *TransferState) { st.Status = TransferRunning })
		err := run(runCtx)
		t.update(func(st *TransferState) {
			switch {
			case err == nil:
				st.Status = TransferCompleted
				st.CurrentPath = ""
			case runCtx.Err() != nil:
				st.Status = TransferCancelled
				st.Error = "SFTP transfer was cancelled"
			default:
				st.Status = TransferFailed
				st.Error = err.Error()
			}
		})
	}()
	return initial, nil
}

func (s *Session) addTransfer(t *transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closedError()
	}
	if len(s.transfers) >= maxTransfers {
		i := slices.IndexFunc(s.transfers, (*transfer).finished)
		if i < 0 {
			return fmt.Errorf("SSH session %s already runs %d SFTP transfers; wait for one to finish or cancel one", s.ID, maxTransfers)
		}
		s.transfers = slices.Delete(s.transfers, i, i+1)
	}
	s.transfers = append(s.transfers, t)
	return nil
}

func (s *Session) planUpload(c *sftp.Client, t *transfer, req TransferRequest) (func(context.Context) error, error) {
	info, err := os.Lstat(req.Local)
	if err != nil {
		return nil, fmt.Errorf("local stat failed: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("SFTP upload does not support local symlinks")
	}
	if info.IsDir() && !req.Recursive {
		return nil, errors.New("recursive is required to upload a directory")
	}
	name := filepath.Base(req.Local)
	if !safeName(name) {
		name = "upload"
	}
	base := joinRemote(req.Remote, name)
	var dirs []string
	var files []localFile
	if info.IsDir() {
		err := filepath.WalkDir(req.Local, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == req.Local {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil // symlinks inside the tree are not followed
			}
			rel, err := filepath.Rel(req.Local, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				dirs = append(dirs, rel)
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if len(files) >= maxTransferFiles {
				return fmt.Errorf("the directory has more than %d files", maxTransferFiles)
			}
			files = append(files, localFile{abs: p, rel: rel, size: fi.Size()})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("local list failed: %v", err)
		}
	} else {
		files = []localFile{{abs: req.Local, size: info.Size()}}
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	t.state.Direction = "upload"
	t.state.SourcePath = req.Display(req.Local)
	t.state.TargetPath = base
	t.state.CurrentPath = t.state.SourcePath
	t.state.BytesTotal = total
	t.state.FilesTotal = len(files)
	isDir := info.IsDir()
	return func(ctx context.Context) error {
		if isDir {
			if err := ensureRemoteDirs(c, base); err != nil {
				return err
			}
			for _, d := range dirs {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := ensureRemoteDirs(c, joinRemote(base, d)); err != nil {
					return err
				}
			}
		}
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			target := base
			if isDir {
				target = joinRemote(base, f.rel)
			}
			t.update(func(st *TransferState) { st.CurrentPath = target })
			if !req.Overwrite {
				exists, err := remoteExists(c, target)
				if err != nil {
					return err
				}
				if exists {
					return fmt.Errorf("target already exists: %s", target)
				}
			}
			if parent, ok := remoteParent(target); ok {
				if err := ensureRemoteDirs(c, parent); err != nil {
					return err
				}
			}
			if err := uploadFile(ctx, c, t, f.abs, target); err != nil {
				return err
			}
			t.update(func(st *TransferState) { st.FilesDone++ })
		}
		return nil
	}, nil
}

func uploadFile(ctx context.Context, c *sftp.Client, t *transfer, local, remote string) error {
	src, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("failed to open local file: %v", err)
	}
	defer src.Close()
	dst, err := c.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("failed to create remote file: %v", err)
	}
	if _, err := io.Copy(dst, progressReader{ctx, src, t}); err != nil {
		dst.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("failed to write remote file: %v", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("failed to close remote file: %v", err)
	}
	return nil
}

func (s *Session) planDownload(ctx context.Context, c *sftp.Client, t *transfer, req TransferRequest) (func(context.Context) error, error) {
	source := NormalizeRemotePath(req.Remote)
	info, err := c.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("remote stat failed: %v", err)
	}
	if info.IsDir() && !req.Recursive {
		return nil, errors.New("recursive is required to download a directory")
	}
	name := remoteBase(source)
	if !safeName(name) {
		name = "download"
	}
	base := filepath.Join(req.Local, name)
	var dirs []string
	var files []remoteFile
	if info.IsDir() {
		queue := []string{""}
		for len(queue) > 0 {
			rel := queue[0]
			queue = queue[1:]
			infos, err := c.ReadDirContext(ctx, joinRemote(source, rel))
			if err != nil {
				return nil, fmt.Errorf("remote list failed: %v", err)
			}
			for _, fi := range infos {
				if !safeName(fi.Name()) {
					continue
				}
				childRel := path.Join(rel, fi.Name())
				switch {
				case fi.IsDir():
					dirs = append(dirs, childRel)
					queue = append(queue, childRel)
				case fi.Mode().IsRegular():
					if len(files) >= maxTransferFiles {
						return nil, fmt.Errorf("the directory has more than %d files", maxTransferFiles)
					}
					files = append(files, remoteFile{path: joinRemote(source, childRel), rel: childRel, size: fi.Size()})
				}
			}
		}
	} else {
		files = []remoteFile{{path: source, size: info.Size()}}
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	t.state.Direction = "download"
	t.state.SourcePath = source
	t.state.TargetPath = req.Display(base)
	t.state.CurrentPath = source
	t.state.BytesTotal = total
	t.state.FilesTotal = len(files)
	isDir := info.IsDir()
	return func(ctx context.Context) error {
		if isDir {
			if err := os.MkdirAll(base, 0o755); err != nil {
				return fmt.Errorf("failed to create local folder: %v", err)
			}
			for _, d := range dirs {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Join(base, filepath.FromSlash(d)), 0o755); err != nil {
					return fmt.Errorf("failed to create local folder: %v", err)
				}
			}
		}
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			target := base
			if isDir {
				target = filepath.Join(base, filepath.FromSlash(f.rel))
			}
			display := req.Display(target)
			t.update(func(st *TransferState) { st.CurrentPath = display })
			if fi, err := os.Lstat(target); err == nil {
				if fi.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("target is a local symlink: %s", display)
				}
				if !req.Overwrite {
					return fmt.Errorf("target already exists: %s", display)
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("failed to create local folder: %v", err)
			}
			if err := downloadFile(ctx, c, t, f.path, target); err != nil {
				return err
			}
			t.update(func(st *TransferState) { st.FilesDone++ })
		}
		return nil
	}, nil
}

func downloadFile(ctx context.Context, c *sftp.Client, t *transfer, remote, local string) error {
	src, err := c.Open(remote)
	if err != nil {
		return fmt.Errorf("failed to open remote file: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create local file: %v", err)
	}
	if _, err := io.Copy(progressWriter{ctx, dst, t}, contextReader{ctx, src}); err != nil {
		dst.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("failed to read remote file: %v", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("failed to write local file: %v", err)
	}
	return nil
}

// errTransferNotFound is LiveAgent v1's message for an unknown transfer id.
var errTransferNotFound = errors.New("SFTP transfer not found")

func (s *Session) findTransfer(id string) (*transfer, bool) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.transfers {
		if t.id == id {
			return t, true
		}
	}
	return nil, false
}

// TransferStatus reports a transfer of s.
func (s *Session) TransferStatus(id string) (TransferResponse, error) {
	t, ok := s.findTransfer(id)
	if !ok {
		return TransferResponse{}, errTransferNotFound
	}
	return TransferResponse{Transfer: t.snapshot()}, nil
}

// CancelTransfer stops a running transfer of s and waits briefly for it to
// stop.
func (s *Session) CancelTransfer(id string) error {
	t, ok := s.findTransfer(id)
	if !ok || t.finished() {
		return errTransferNotFound
	}
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}
