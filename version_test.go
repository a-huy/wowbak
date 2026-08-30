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
