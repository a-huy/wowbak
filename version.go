package main

import (
	"strconv"
	"strings"
)

// compareVersions orders two addon version strings. WoW addons use no single
// scheme - "v12.0.21", "335", "1.99422", "12.1.0.7", "1.2.3-4-gabc123" and plain
// dates all occur - so this compares run-by-run rather than assuming semver.
//
// Returns -1 if a < b, 0 if equal, 1 if a > b, and false when either side has
// nothing comparable in it.
func compareVersions(a, b string) (int, bool) {
	ta, tb := strip(versionTokens(a)), strip(versionTokens(b))
	if len(ta) == 0 || len(tb) == 0 {
		return 0, false
	}

	for i := 0; i < len(ta) || i < len(tb); i++ {
		// One version ran out. A trailing numeric run means a longer release
		// (1.0.1 > 1.0); a trailing word means a pre-release (1.0 > 1.0-beta).
		if i >= len(ta) {
			if tb[i].numeric {
				return -1, true
			}
			return 1, true
		}
		if i >= len(tb) {
			if ta[i].numeric {
				return 1, true
			}
			return -1, true
		}

		x, y := ta[i], tb[i]
		switch {
		case x.numeric && y.numeric:
			if x.num != y.num {
				return sign(x.num - y.num), true
			}
		case !x.numeric && !y.numeric:
			if x.text != y.text {
				return strings.Compare(x.text, y.text), true
			}
		case x.numeric:
			return 1, true // a release beats a pre-release word
		default:
			return -1, true
		}
	}
	return 0, true
}

// flavorWords name a game version or a build type rather than a version. They
// appear in both .toc versions and release tags ("Plater-v653-Retail" against
// tag "Plater-v653") and must not be read as pre-release markers, or an addon
// looks permanently one step behind itself.
//
// Genuine pre-release words - alpha, beta, rc - are deliberately absent: those
// really do order before a release and must keep doing so.
var flavorWords = map[string]bool{
	"retail": true, "mainline": true, "release": true, "classic": true,
	"era": true, "classicera": true, "vanilla": true, "hardcore": true,
	"bc": true, "bcc": true, "tbc": true, "wrath": true, "wotlk": true,
	"cata": true, "cataclysm": true, "mists": true, "mop": true,
	"sod": true, "titan": true, "wow": true,
}

// strip drops flavor words so two spellings of the same version compare equal.
func strip(ts []vtoken) []vtoken {
	out := ts[:0:0]
	for _, t := range ts {
		if !t.numeric && flavorWords[t.text] {
			continue
		}
		out = append(out, t)
	}
	return out
}

type vtoken struct {
	numeric bool
	num     int64
	text    string
}

// versionTokens splits a version into alternating numeric and word runs,
// ignoring separators and a leading "v".
func versionTokens(s string) []vtoken {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "v")

	var out []vtoken
	var cur strings.Builder
	curNumeric := false

	flush := func() {
		if cur.Len() == 0 {
			return
		}
		text := cur.String()
		if curNumeric {
			// Very long digit runs cannot fit an int64; compare them as text,
			// which is still stable for equal-length numbers.
			if n, err := strconv.ParseInt(text, 10, 64); err == nil {
				out = append(out, vtoken{numeric: true, num: n})
			} else {
				out = append(out, vtoken{text: text})
			}
		} else {
			out = append(out, vtoken{text: text})
		}
		cur.Reset()
	}

	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			if !curNumeric {
				flush()
				curNumeric = true
			}
			cur.WriteRune(r)
		case r >= 'a' && r <= 'z':
			if curNumeric {
				flush()
				curNumeric = false
			}
			cur.WriteRune(r)
		default:
			flush()
			curNumeric = false
		}
	}
	flush()
	return out
}

func sign(n int64) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// newerThan reports whether candidate is a later version than current. It is
// deliberately conservative: when the two cannot be compared it returns false,
// so an ambiguous version never triggers an unwanted update.
func newerThan(candidate, current string) bool {
	cmp, ok := compareVersions(candidate, current)
	return ok && cmp > 0
}
