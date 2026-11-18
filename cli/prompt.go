// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qiangli/bash/shell"
)

// expandPrompt expands Bash-style prompt escape sequences in s, then performs
// parameter/arithmetic expansion on the result, and — in posix mode — the
// posix bare-`!` history expansions. See Bash manual section 6.9 "Controlling
// the Prompt" and the "Bash POSIX Mode" item on PS1/PS2 (#29): bash performs
// parameter expansion on PS1/PS2 regardless of promptvars, and in posix mode a
// bare `!` expands to the history number and `!!` to a literal `!`.
//
// Order matches bash: backslash escapes are decoded first (so `\!` -> history
// number and `\$` -> $/# survive), then word expansion runs (so `$!` -> last
// bg pid, `${v}` -> value, `$((..))` -> n), then the posix bare-`!` pass runs
// on what remains (so the `!` in `$!` is already consumed and not re-touched).
func expandPrompt(s string, env func(string) string, cmdNum, histNum int, posix bool) string {
	decoded := decodePromptEscapes(s, env, cmdNum, histNum)
	// Parameter/arithmetic expansion (promptvars is effectively always on for
	// us; posix forces it). shell.Expand uses Document semantics (no field
	// splitting). Command substitution needs a runner it doesn't have here, so
	// fall back to the unexpanded form on any parse/expand error rather than
	// dropping the prompt.
	if env != nil {
		if expanded, err := shell.Expand(decoded, env); err == nil {
			decoded = expanded
		}
	}
	if posix {
		decoded = posixBangExpand(decoded, histNum)
	}
	return decoded
}

// posixBangExpand applies the posix-mode prompt rule: `!!` -> `!`, and a bare
// `!` -> the history number.
func posixBangExpand(s string, histNum int) string {
	if !strings.ContainsRune(s, '!') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '!' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '!' {
			b.WriteByte('!')
			i++
		} else {
			b.WriteString(strconv.Itoa(histNum))
		}
	}
	return b.String()
}

// decodePromptEscapes expands the backslash escape sequences (\u, \h, \w, \!,
// \$, …) in s. It is the first stage of expandPrompt.
func decodePromptEscapes(s string, env func(string) string, cmdNum, histNum int) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			b.WriteByte('\\')
			break
		}
		switch s[i] {
		case 'a':
			b.WriteByte('\a')
		case 'd':
			b.WriteString(time.Now().Format("Mon Jan 02"))
		case 'D':
			// \D{FORMAT} — strftime-style format
			if i+1 < len(s) && s[i+1] == '{' {
				end := strings.IndexByte(s[i+1:], '}')
				if end >= 0 {
					format := s[i+2 : i+1+end]
					if format == "" {
						b.WriteString(time.Now().Format("15:04:05"))
					} else {
						b.WriteString(strftimeSimple(format))
					}
					i += 1 + end
					continue
				}
			}
			b.WriteString("\\D")
		case 'e':
			b.WriteByte('\x1b')
		case 'h':
			h, _ := os.Hostname()
			if dot := strings.IndexByte(h, '.'); dot >= 0 {
				h = h[:dot]
			}
			b.WriteString(h)
		case 'H':
			h, _ := os.Hostname()
			b.WriteString(h)
		case 'j':
			// Number of jobs — we don't track this in prompt.go,
			// so default to 0. The caller can replace this.
			b.WriteByte('0')
		case 'l':
			// Basename of the terminal device name.
			name := "tty"
			if link, err := os.Readlink("/dev/fd/0"); err == nil {
				name = filepath.Base(link)
			}
			b.WriteString(name)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 's':
			b.WriteString("bashy")
		case 't':
			b.WriteString(time.Now().Format("15:04:05"))
		case 'T':
			b.WriteString(time.Now().Format("03:04:05"))
		case '@':
			b.WriteString(time.Now().Format("03:04 PM"))
		case 'A':
			b.WriteString(time.Now().Format("15:04"))
		case 'u':
			if u, err := user.Current(); err == nil {
				b.WriteString(u.Username)
			}
		case 'v':
			b.WriteString(bashVerMajor + "." + bashVerMinor)
		case 'V':
			b.WriteString(bashVerMajor + "." + bashVerMinor + "." + bashVerPatch)
		case 'w':
			pwd := env("PWD")
			home := env("HOME")
			if home != "" && strings.HasPrefix(pwd, home) {
				pwd = "~" + pwd[len(home):]
			}
			if n, err := strconv.Atoi(env("PROMPT_DIRTRIM")); err == nil && n > 0 {
				pwd = trimPromptDir(pwd, n)
			}
			b.WriteString(pwd)
		case 'W':
			pwd := env("PWD")
			home := env("HOME")
			if pwd == home {
				b.WriteByte('~')
			} else {
				b.WriteString(filepath.Base(pwd))
			}
		case '!':
			b.WriteString(strconv.Itoa(histNum))
		case '#':
			b.WriteString(strconv.Itoa(cmdNum))
		case '$':
			if os.Geteuid() == 0 {
				b.WriteByte('#')
			} else {
				b.WriteByte('$')
			}
		case '\\':
			b.WriteByte('\\')
		case '[':
			// Begin non-printing sequence (ignored in non-readline mode).
		case ']':
			// End non-printing sequence (ignored in non-readline mode).
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// Octal character code \NNN
			end := i + 1
			for end < len(s) && end < i+3 && s[end] >= '0' && s[end] <= '7' {
				end++
			}
			val, _ := strconv.ParseUint(s[i:end], 8, 8)
			b.WriteByte(byte(val))
			i = end - 1
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// trimPromptDir keeps the last n components of pwd, prepending ".../"
// when truncation occurred. A leading "~" or "/" is preserved verbatim:
// trim("~/a/b/c/d", 2) → "~/.../c/d", trim("/a/b/c/d", 2) → "/.../c/d".
func trimPromptDir(pwd string, n int) string {
	var prefix string
	rest := pwd
	switch {
	case strings.HasPrefix(rest, "~/"):
		prefix, rest = "~/", rest[2:]
	case rest == "~":
		return rest
	case strings.HasPrefix(rest, "/"):
		prefix, rest = "/", rest[1:]
	}
	parts := strings.Split(rest, "/")
	if len(parts) <= n {
		return pwd
	}
	return prefix + ".../" + strings.Join(parts[len(parts)-n:], "/")
}

// strftimeSimple converts a limited set of strftime format specifiers
// to Go time format strings and formats the current time.
func strftimeSimple(format string) string {
	// Map common strftime specifiers to Go format.
	replacer := strings.NewReplacer(
		"%Y", "2006",
		"%m", "01",
		"%d", "02",
		"%H", "15",
		"%M", "04",
		"%S", "05",
		"%p", "PM",
		"%I", "03",
		"%A", "Monday",
		"%a", "Mon",
		"%B", "January",
		"%b", "Jan",
		"%Z", "MST",
		"%z", "-0700",
		"%T", "15:04:05",
		"%R", "15:04",
		"%r", "03:04:05 PM",
		"%%", "%",
	)
	goFmt := replacer.Replace(format)
	result := time.Now().Format(goFmt)
	// Handle %e (day without leading zero) as a post-process.
	if strings.Contains(format, "%e") {
		day := fmt.Sprintf("%2d", time.Now().Day())
		result = strings.ReplaceAll(result, time.Now().Format("02"), day)
	}
	return result
}
