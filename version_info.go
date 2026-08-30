package main

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// Stamped at build time with -ldflags "-X main.version=v1.2.3 ...".
// A build made without them reports itself as a development build, which is
// what stops self-update from offering to "upgrade" a local build to a release.
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

// repoSlug is where releases are published and where self-update looks.
const repoSlug = "a-huy/wowbak"

// pseudoVersion matches the synthetic versions Go invents for untagged commits,
// e.g. v0.0.0-20260830063415-cdd488a15fc9.
var pseudoVersion = regexp.MustCompile(`-\d{14}-[0-9a-f]{12}`)

func versionString() string {
	if version != "dev" && version != "" {
		return version
	}
	// A `go install`ed build knows its module version, but only a real tag is
	// worth reporting. A pseudo-version or a dirty tree is still a local build,
	// and calling it a release would make self-update compare nonsense.
	if bi, ok := debug.ReadBuildInfo(); ok {
		v := bi.Main.Version
		if v != "" && v != "(devel)" &&
			!strings.Contains(v, "+dirty") &&
			!strings.HasPrefix(v, "v0.0.0-") &&
			!pseudoVersion.MatchString(v) {
			return v
		}
	}
	return "dev"
}

func isDevBuild() bool { return versionString() == "dev" }

func cmdVersion() int {
	fmt.Printf("wowbak %s\n", versionString())
	if commit != "" {
		fmt.Printf("  commit  %s\n", commit)
	}
	if buildDate != "" {
		fmt.Printf("  built   %s\n", buildDate)
	}
	fmt.Printf("  go      %s\n", runtime.Version())
	fmt.Printf("  target  %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  source  https://github.com/%s\n", repoSlug)
	if isDevBuild() {
		fmt.Println("\nThis is a local build, so it will not offer to update itself.")
	}
	return 0
}
