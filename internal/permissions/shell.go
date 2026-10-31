package permissions

import (
	"regexp"
	"strings"
	"unicode"
)

type shellKind int

const (
	shellUnknown shellKind = iota
	shellPosix
	shellPwsh
	shellCmd
)

func shellKindOf(shell string) shellKind {
	name := strings.TrimSpace(shell)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	switch normCommand(name) {
	case "bash", "sh", "zsh", "dash", "ksh":
		return shellPosix
	case "pwsh", "powershell", "powershell7":
		return shellPwsh
	case "cmd":
		return shellCmd
	}
	return shellUnknown
}

// SplitCommand splits a shell command into the simple commands it runs, each as its
// words with quotes removed. ok is false whenever the command could run something
// the words do not show: command or process substitution, subshells and script
// blocks, control keywords, redirection to anything but the null device, input
// redirection, comments, leading variable assignments, and wrappers such as sudo,
// xargs, eval or `bash -c`.
//
// Unknown shells are parsed strictly: a construct that separates commands in any
// supported shell separates them here, and anything the shells read differently
// fails.
func SplitCommand(command, shell string) ([][]string, bool) {
	kind := shellKindOf(shell)
	if (kind == shellPwsh || kind == shellUnknown) && hasPwshLookalike(command) {
		return nil, false
	}
	s := splitter{kind: kind, runes: []rune(command)}
	if !s.run() || len(s.segments) == 0 {
		return nil, false
	}
	for _, seg := range s.segments {
		if !plainSegment(seg, kind) {
			return nil, false
		}
	}
	return s.segments, true
}

// hasPwshLookalike reports characters PowerShell reads as quotes, dashes or blanks
// although they look like ordinary text: typographic quotes close its strings and
// en dashes introduce parameters.
func hasPwshLookalike(command string) bool {
	for _, r := range command {
		switch {
		case r >= '‘' && r <= '„', r >= '–' && r <= '―':
			return true
		case r > unicode.MaxASCII && unicode.IsSpace(r):
			return true
		}
	}
	return false
}

type splitter struct {
	kind     shellKind
	runes    []rune
	i        int
	segments [][]string
	cur      []string
	tok      strings.Builder
	inTok    bool
}

func (s *splitter) peek(offset int) rune {
	if j := s.i + offset; j < len(s.runes) {
		return s.runes[j]
	}
	return 0
}

func (s *splitter) write(r rune) {
	s.tok.WriteRune(r)
	s.inTok = true
}

func (s *splitter) flush() bool {
	if !s.inTok {
		return true
	}
	word := s.tok.String()
	if word == "--%" && s.kind != shellPosix && s.kind != shellCmd {
		return false // PowerShell stop-parsing token: the rest is passed through raw
	}
	s.cur = append(s.cur, word)
	s.tok.Reset()
	s.inTok = false
	return true
}

func (s *splitter) end() bool {
	if !s.flush() {
		return false
	}
	if len(s.cur) > 0 {
		s.segments = append(s.segments, s.cur)
		s.cur = nil
	}
	return true
}

func (s *splitter) run() bool {
	for ; s.i < len(s.runes); s.i++ {
		r := s.runes[s.i]
		switch {
		case r == '\n':
			if !s.end() {
				return false
			}
		case r == '\r':
			// PowerShell ends a statement at a bare CR; CRLF is one line break.
			if s.peek(1) != '\n' && !s.end() {
				return false
			}
		case r == ' ' || r == '\t':
			if !s.flush() {
				return false
			}
		case r == '\\' && s.kind == shellPosix,
			r == '`' && s.kind == shellPwsh,
			r == '^' && s.kind == shellCmd:
			next := s.peek(1)
			if next == 0 {
				return false
			}
			s.i++
			if next == '\n' {
				continue // line continuation
			}
			s.write(next)
		case r == '`':
			return false // posix or unknown: command substitution
		case r == '^' && s.kind == shellUnknown && s.peek(1) == '"':
			return false // cmd reads ^" as a literal quote, the others open a string
		case r == '\'' && s.kind != shellCmd:
			s.inTok = true
			if !s.single() {
				return false
			}
		case r == '"':
			s.inTok = true
			if !s.double() {
				return false
			}
		case r == '#' && !s.inTok && s.kind != shellCmd:
			return false // comment: whatever follows is not run, but we do not guess
		case r == ';':
			if !s.end() {
				return false
			}
		case r == '|':
			if n := s.peek(1); n == '|' || n == '&' {
				s.i++
			}
			if !s.end() {
				return false
			}
		case r == '&':
			if s.peek(1) == '>' {
				s.i++
				if !s.redirect() {
					return false
				}
				continue
			}
			if s.peek(1) == '&' {
				s.i++
			}
			if !s.end() {
				return false
			}
		case r == '>' || r == '<':
			if s.inTok {
				if !isRedirectFD(s.tok.String()) {
					return false
				}
				s.tok.Reset()
				s.inTok = false
			}
			if r == '<' {
				return false // input redirection and here-documents
			}
			if !s.redirect() {
				return false
			}
		case r == '(' || r == ')' || r == '{' || r == '}':
			return false
		case r == '$' && s.peek(1) == '(':
			return false
		default:
			s.write(r)
		}
	}
	return s.end()
}

func isRedirectFD(fd string) bool {
	if fd == "*" {
		return true
	}
	for _, r := range fd {
		if r < '0' || r > '9' {
			return false
		}
	}
	return fd != ""
}

func endsWord(r rune) bool {
	return r == 0 || r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == ';' || r == '|' || r == '&'
}

// redirect consumes an output redirection whose `>` is at s.i. Only duplicating a
// descriptor (2>&1, >&-) and discarding to the null device are accepted.
func (s *splitter) redirect() bool {
	if n := s.peek(1); n == '>' || (n == '|' && s.kind == shellPosix) {
		s.i++
	}
	if s.peek(1) == '&' {
		// In bash `>&word` writes to the file `word` unless word is a descriptor.
		s.i += 2
		start := s.i
		if s.i < len(s.runes) && s.runes[s.i] == '-' {
			s.i++
		} else {
			for s.i < len(s.runes) && s.runes[s.i] >= '0' && s.runes[s.i] <= '9' {
				s.i++
			}
		}
		if s.i == start || !endsWord(s.peek(0)) {
			return false
		}
		s.i--
		return true
	}
	s.i++
	for s.i < len(s.runes) && (s.runes[s.i] == ' ' || s.runes[s.i] == '\t') {
		s.i++
	}
	start := s.i
	for s.i < len(s.runes) && !strings.ContainsRune(" \t\r\n;|&<>(){}'\"`", s.runes[s.i]) {
		s.i++
	}
	target := strings.ToLower(string(s.runes[start:s.i]))
	s.i--
	switch target {
	case "/dev/null", "$null", "nul":
		return true
	}
	return false
}

func (s *splitter) single() bool {
	for s.i++; s.i < len(s.runes); s.i++ {
		r := s.runes[s.i]
		if r == '\'' {
			if s.kind == shellPwsh && s.peek(1) == '\'' {
				s.i++
				s.tok.WriteRune('\'')
				continue
			}
			return true
		}
		if s.kind == shellUnknown && strings.ContainsRune("\"&|<>^%\r\n", r) {
			return false // cmd does not quote with ', so these would act there
		}
		s.tok.WriteRune(r)
	}
	return false
}

func (s *splitter) double() bool {
	for s.i++; s.i < len(s.runes); s.i++ {
		r := s.runes[s.i]
		switch {
		case r == '"':
			if s.kind == shellPwsh && s.peek(1) == '"' {
				s.i++
				s.tok.WriteRune('"')
				continue
			}
			return true
		case r == '\\' && s.kind == shellPosix:
			next := s.peek(1)
			if strings.ContainsRune("$`\"\\\n", next) && next != 0 {
				s.i++
				if next != '\n' {
					s.tok.WriteRune(next)
				}
				continue
			}
			s.tok.WriteRune(r)
		case r == '\\' && s.kind == shellUnknown && s.peek(1) == '"':
			return false // escaped in posix, closing in PowerShell and cmd
		case r == '`' && s.kind == shellPwsh:
			next := s.peek(1)
			if next == 0 {
				return false
			}
			s.i++
			s.tok.WriteRune(next)
		case r == '`' && s.kind != shellCmd:
			return false
		case r == '$' && s.peek(1) == '(' && s.kind != shellCmd:
			return false
		default:
			s.tok.WriteRune(r)
		}
	}
	return false
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// wrappers run another command given as their arguments, so the words no longer say
// what runs.
var wrappers = map[string]bool{
	"eval": true, "exec": true, "source": true, ".": true, "iex": true,
	"invoke-expression": true, "invoke-command": true, "icm": true, "start-process": true,
	"saps": true, "start": true, "sudo": true, "doas": true, "su": true, "env": true,
	"xargs": true, "nohup": true, "time": true, "timeout": true, "nice": true,
	"ionice": true, "command": true, "builtin": true, "watch": true, "parallel": true,
	"call": true, "runas": true, "chroot": true, "setsid": true, "stdbuf": true,
	"strace": true, "nsenter": true, "unbuffer": true, "script": true, "busybox": true,
}

// keywords start compound commands; the word after them is the command that runs.
var keywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "do": true,
	"done": true, "case": true, "esac": true, "for": true, "while": true, "until": true,
	"select": true, "function": true, "!": true, "[[": true, "]]": true, "coproc": true,
	"foreach": true, "switch": true, "filter": true, "trap": true, "try": true,
	"catch": true, "finally": true, "param": true, "begin": true, "process": true,
	"end": true, "return": true, "throw": true,
}

func plainSegment(seg []string, kind shellKind) bool {
	first := seg[0]
	if first == "" || strings.HasPrefix(first, "$") || strings.HasPrefix(first, "&") {
		return false
	}
	if kind != shellCmd && assignment.MatchString(first) {
		return false
	}
	name := normCommand(first)
	if wrappers[name] || keywords[name] {
		return false
	}
	args := seg[1:]
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		if len(args) == 0 {
			return false // reads commands from stdin
		}
		for _, arg := range args {
			if arg == "-" || arg == "--command" || (strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsAny(arg, "cs")) {
				return false
			}
		}
	case "pwsh", "powershell", "powershell7":
		if len(args) == 0 {
			return false
		}
		for _, arg := range args {
			a := strings.ToLower(arg)
			if a == "-" || strings.HasPrefix(a, "-c") || strings.HasPrefix(a, "-e") || strings.HasPrefix(a, "/c") || strings.HasPrefix(a, "/e") {
				return false
			}
		}
	case "cmd":
		if len(args) == 0 {
			return false
		}
		for _, arg := range args {
			a := strings.ToLower(arg)
			if strings.HasPrefix(a, "/c") || strings.HasPrefix(a, "/k") || strings.HasPrefix(a, "/r") {
				return false
			}
		}
	case "wsl":
		return len(args) == 0
	}
	return true
}
