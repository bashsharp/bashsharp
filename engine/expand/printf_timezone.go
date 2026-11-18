package expand

import (
	"encoding/binary"
	"strings"
	"time"
	_ "time/tzdata" // IANA TZ values must work even without host zoneinfo files.
)

// printfLocationFromTZ resolves an explicitly set TZ. The POSIX rule path
// uses Go's own tzset evaluator through a small TZif wrapper; it does not map
// a rule to an unrelated IANA region. This mirrors the resolver used by the
// pure-Go date applet without making the shell engine depend on coreutils.
func printfLocationFromTZ(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	if name, ok := strings.CutPrefix(tz, ":"); ok {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
		return time.UTC
	}
	if std, offset, ok := printfPOSIXStandard(tz); ok {
		if loc, err := time.LoadLocationFromTZData(tz, printfTZif(tz, std, offset)); err == nil {
			return loc
		}
		return time.FixedZone(std, offset)
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// printfPOSIXStandard parses the standard-zone prefix of a POSIX TZ value.
// POSIX offsets are west-positive; time.Location offsets are east-positive.
func printfPOSIXStandard(tz string) (string, int, bool) {
	name, rest, ok := printfTZName(tz)
	if !ok {
		return "", 0, false
	}
	west, _, ok := printfTZOffset(rest)
	if !ok {
		return "", 0, false
	}
	return name, -west, true
}

func printfTZName(s string) (name, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '<' {
		if i := strings.IndexByte(s, '>'); i > 1 {
			return s[1:i], s[i+1:], true
		}
		return "", "", false
	}
	i := 0
	for i < len(s) && !strings.ContainsRune("0123456789,-+", rune(s[i])) {
		i++
	}
	if i < 3 {
		return "", "", false
	}
	return s[:i], s[i:], true
}

func printfTZOffset(s string) (seconds int, rest string, ok bool) {
	negative := false
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		negative = s[0] == '-'
		s = s[1:]
	}
	hours, s, ok := printfTZNumber(s, 167)
	if !ok {
		return 0, "", false
	}
	seconds = hours * 3600
	for _, scale := range []int{60, 1} {
		if len(s) == 0 || s[0] != ':' {
			break
		}
		var part int
		part, s, ok = printfTZNumber(s[1:], 59)
		if !ok {
			return 0, "", false
		}
		seconds += part * scale
	}
	if negative {
		seconds = -seconds
	}
	return seconds, s, true
}

func printfTZNumber(s string, max int) (number int, rest string, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' && i < 3 {
		number = number*10 + int(s[i]-'0')
		i++
	}
	if i == 0 || number > max {
		return 0, "", false
	}
	return number, s[i:], true
}

// printfTZif wraps the POSIX rule in a TZif v2 extension. A single standard
// zone supplies the fallback; Go evaluates the rule for practical timestamps.
func printfTZif(spec, standard string, standardOffset int) []byte {
	abbreviation := append([]byte(standard), 0)
	var data []byte
	header := func(transitions uint32) {
		data = append(data, "TZif2"...)
		data = append(data, make([]byte, 15)...)
		for _, count := range []uint32{0, 0, 0, transitions, 1, uint32(len(abbreviation))} {
			data = binary.BigEndian.AppendUint32(data, count)
		}
	}
	zone := func() {
		data = binary.BigEndian.AppendUint32(data, uint32(int32(standardOffset)))
		data = append(data, 0, 0)
		data = append(data, abbreviation...)
	}
	header(0)
	zone()
	header(1)
	when := int64(-1) << 59
	data = binary.BigEndian.AppendUint64(data, uint64(when))
	data = append(data, 0)
	zone()
	data = append(data, '\n')
	data = append(data, spec...)
	data = append(data, '\n')
	return data
}
