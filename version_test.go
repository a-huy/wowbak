package main

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.5.1", "1.4.0", 1, true},
		{"1.4.0", "1.5.1", -1, true},
		{"1.5.1", "1.5.1", 0, true},
		{"v12.0.21", "12.0.22", -1, true},
		{"v12.0.21", "12.0.21", 0, true}, // leading v is noise
		{"335", "336", -1, true},
		{"1.99422", "1.99423", -1, true},
		{"12.1.0.7", "12.1.0.10", -1, true}, // numeric, not lexical
		{"12.1.0.10", "12.1.0.7", 1, true},
		{"1.0.0", "1.0.0-beta", 1, true}, // release beats pre-release
		{"1.0.0-beta", "1.0.0-alpha", 1, true},
		{"1.0.1", "1.0", 1, true},
		{"2024.05.01", "2024.06.01", -1, true},
		{"1.2.3-4-gabc123", "1.2.4", -1, true},
		{"r123", "r124", -1, true},
		{"5.21.1", "5.22.0", -1, true},
		// Flavor suffixes are noise, not pre-release markers.
		{"Plater-v653-Retail", "Plater-v653", 0, true},
		{"Plater-v653", "Plater-v653-Retail", 0, true},
		{"v5.0.14-release", "5.0.14", 0, true},
		{"1.2.3-Classic", "1.2.3", 0, true},
		{"1.2.3-Mainline", "1.2.4", -1, true},
		// ...but real pre-release markers still order before a release.
		{"1.0.0", "1.0.0-beta", 1, true},
		{"1.0.0-rc", "1.0.0", -1, true},
		{"", "1.0", 0, false},
		{"1.0", "", 0, false},
		{"-", "1.0", 0, false},
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("compareVersions(%q, %q) = %d, %v; want %d, %v",
				c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestNewerThanIsConservative(t *testing.T) {
	if newerThan("", "1.0") {
		t.Error("an uncomparable version must not count as newer")
	}
	if newerThan("1.0", "1.0") {
		t.Error("an equal version must not count as newer")
	}
	if !newerThan("1.1", "1.0") {
		t.Error("1.1 should be newer than 1.0")
	}
}

func TestVersionStringRejectsPseudoVersions(t *testing.T) {
	// A local build must report "dev": if it claimed a version, self-update
	// would compare a synthetic string against real release tags.
	saved := version
	defer func() { version = saved }()

	version = "dev"
	if got := versionString(); got != "dev" {
		t.Errorf("an unstamped build reported %q, want \"dev\"", got)
	}
	if !isDevBuild() {
		t.Error("an unstamped build must count as a development build")
	}

	version = "v1.2.3"
	if got := versionString(); got != "v1.2.3" {
		t.Errorf("a stamped build reported %q, want \"v1.2.3\"", got)
	}
	if isDevBuild() {
		t.Error("a stamped build must not count as a development build")
	}
}

func TestPseudoVersionPattern(t *testing.T) {
	for _, v := range []string{
		"v0.0.0-20260830063415-cdd488a15fc9",
		"v1.2.3-20260830063415-abcdef123456",
	} {
		if !pseudoVersion.MatchString(v) {
			t.Errorf("%q should be recognised as a pseudo-version", v)
		}
	}
	for _, v := range []string{"v1.2.3", "v0.1.0", "v2.0.0-rc1"} {
		if pseudoVersion.MatchString(v) {
			t.Errorf("%q is a real version, not a pseudo-version", v)
		}
	}
}

func TestGitDescribeVersionsCountAsDevBuilds(t *testing.T) {
	saved := version
	defer func() { version = saved }()

	// Built from a working tree: must never self-update, or a local build gets
	// silently replaced by a release.
	for _, v := range []string{
		"dev",
		"v0.1.0-3-g6e2f7fe",
		"v0.2.1-12-gabc1234",
		"v0.1.0-dirty",
		"v0.0.0-20260830063415-cdd488a15fc9",
	} {
		version = v
		if !isDevBuild() {
			t.Errorf("%q should count as a development build", v)
		}
	}

	// Published releases: exact tags only.
	for _, v := range []string{"v0.1.0", "v0.2.1", "v1.0.0", "v2.0.0-rc1"} {
		version = v
		if isDevBuild() {
			t.Errorf("%q is a release and should not count as a development build", v)
		}
	}
}
