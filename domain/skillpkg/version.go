package skillpkg

import (
	"strconv"
	"strings"
)

// Version is a parsed semantic version (MAJOR.MINOR.PATCH[-pre][+build]).
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

// ParseVersion accepts strict SemVer 2.0.0 without a leading "v".
func ParseVersion(s string) (Version, bool) {
	var v Version
	if s == "" || len(s) > 64 {
		return v, false
	}
	core, build, hasBuild := strings.Cut(s, "+")
	if hasBuild && !identifiers(build, false) {
		return v, false
	}
	core, pre, hasPre := strings.Cut(core, "-")
	if hasPre {
		if !identifiers(pre, true) {
			return v, false
		}
		v.Pre = strings.Split(pre, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := make([]uint64, 3)
	for i, p := range parts {
		if !numeric(p) {
			return v, false
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, false
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, true
}

// Compare orders versions by SemVer precedence: -1, 0 or 1.
func Compare(a, b Version) int {
	for _, d := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		if x == y {
			continue
		}
		xn, yn := numeric(x), numeric(y)
		switch {
		case xn && yn:
			xi, _ := strconv.ParseUint(x, 10, 64)
			yi, _ := strconv.ParseUint(y, 10, 64)
			if xi < yi {
				return -1
			}
			return 1
		case xn:
			return -1
		case yn:
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// Newer reports whether next is strictly greater than current; an
// unparsable current (never stored) counts as no current.
func Newer(next, current string) bool {
	n, ok := ParseVersion(next)
	if !ok {
		return false
	}
	c, ok := ParseVersion(current)
	return !ok || Compare(n, c) > 0
}

func numeric(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func identifiers(s string, noLeadingZero bool) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		allDigits := true
		for _, r := range id {
			switch {
			case r >= '0' && r <= '9':
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-':
				allDigits = false
			default:
				return false
			}
		}
		if noLeadingZero && allDigits && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}
