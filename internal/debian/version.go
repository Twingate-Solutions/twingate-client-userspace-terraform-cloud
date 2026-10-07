package debian

import (
	"strconv"
	"strings"
)

// CompareVersions compares two Debian version strings using the dpkg
// algorithm (epoch, upstream version, revision). It returns -1, 0 or 1.
func CompareVersions(a, b string) int {
	ea, ua, ra := splitVersion(a)
	eb, ub, rb := splitVersion(b)
	if ea != eb {
		if ea < eb {
			return -1
		}
		return 1
	}
	if c := compareFragment(ua, ub); c != 0 {
		return c
	}
	return compareFragment(ra, rb)
}

func splitVersion(v string) (epoch int, upstream, revision string) {
	if e, rest, ok := strings.Cut(v, ":"); ok {
		if n, err := strconv.Atoi(e); err == nil {
			epoch = n
			v = rest
		}
	}
	if i := strings.LastIndex(v, "-"); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

// compareFragment implements dpkg's verrevcmp: alternating runs of non-digits
// (compared by order()) and digits (compared numerically).
func compareFragment(a, b string) int {
	for a != "" || b != "" {
		firstDiff := 0
		for (a != "" && !isDigit(a[0])) || (b != "" && !isDigit(b[0])) {
			ac, bc := 0, 0
			if a != "" {
				ac = order(a[0])
			}
			if b != "" {
				bc = order(b[0])
			}
			if ac != bc {
				return sign(ac - bc)
			}
			if a != "" {
				a = a[1:]
			}
			if b != "" {
				b = b[1:]
			}
		}
		for a != "" && a[0] == '0' {
			a = a[1:]
		}
		for b != "" && b[0] == '0' {
			b = b[1:]
		}
		for a != "" && isDigit(a[0]) && b != "" && isDigit(b[0]) {
			if firstDiff == 0 {
				firstDiff = int(a[0]) - int(b[0])
			}
			a, b = a[1:], b[1:]
		}
		if a != "" && isDigit(a[0]) {
			return 1
		}
		if b != "" && isDigit(b[0]) {
			return -1
		}
		if firstDiff != 0 {
			return sign(firstDiff)
		}
	}
	return 0
}

func order(c byte) int {
	switch {
	case isDigit(c):
		return 0
	case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		return int(c)
	case c == '~':
		return -1
	default:
		return int(c) + 256
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
