package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// releaseAssetName maps this build's platform to the asset published for it.
// The names match the portable folder, so a file on the stick can be matched to
// its replacement.
func releaseAssetName() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "wowbak.exe"
	case "windows/arm64":
		return "wowbak-arm64.exe"
	case "darwin/arm64":
		return "wowbak-macos"
	case "darwin/amd64":
		return "wowbak-macos-intel"
	case "linux/amd64":
		return "wowbak-linux"
	case "linux/arm64":
		return "wowbak-linux-arm64"
	}
	return ""
}

// updatableSiblings are the shipped files that can be refreshed in place. A
// stick is used on several machines, so refreshing only the running binary
// would leave the others behind.
var updatableSiblings = []string{
	"wowbak.exe", "wowbak-arm64.exe", "wowbak-macos", "wowbak-macos-intel",
	"wowbak-linux", "wowbak-linux-arm64", "WowBackup.exe",
}

type updateInfo struct {
	Current   string
	Latest    string
	Available bool
	Asset     ghAsset
	Notes     string
	URL       string
}

// checkSelfUpdate asks the repository for its newest release.
func checkSelfUpdate(gh *ghClient) (*updateInfo, error) {
	rels, err := gh.releases(repoSlug)
	if err != nil {
		return nil, err
	}
	info := &updateInfo{Current: versionString()}
	for _, rel := range rels {
		if rel.Draft || rel.Prerelease {
			continue
		}
		info.Latest = rel.TagName
		info.URL = rel.HTMLURL
		if name := releaseAssetName(); name != "" {
			if a, ok := rel.asset(name); ok {
				info.Asset = a
			}
		}
		// A development build has no version to compare, so never claim it is
		// out of date - that would nag on every local build.
		info.Available = !isDevBuild() && newerThan(rel.TagName, info.Current)
		return info, nil
	}
	return info, nil
}

func sha256Of(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// replaceBinary swaps a file that may be running right now.
//
// A running executable cannot be overwritten on Windows, but it can be renamed,
// so the old one is moved aside and deleted later. The new file is fully written
// and checked before anything is moved, so an interrupted download can never
// leave an unusable binary in place.
func replaceBinary(target, newFile string) error {
	if err := os.Chmod(newFile, 0o755); err != nil {
		return err
	}
	old := target + ".old"
	os.Remove(old)

	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, old); err != nil {
			return fmt.Errorf("could not move the old version aside: %w", err)
		}
	}
	if err := os.Rename(newFile, target); err != nil {
		os.Rename(old, target) // put it back
		return fmt.Errorf("could not put the new version in place: %w", err)
	}
	// Fails while the old binary is still running on Windows; cleaned up at the
	// next start instead.
	os.Remove(old)
	return nil
}

// cleanStaleBinaries removes leftovers from a previous self-update. Called at
// startup, when the replaced binary is no longer running.
func cleanStaleBinaries() {
	dir := exeDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".old") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func cmdSelfUpdate(args selfUpdateArgs) int {
	cfg := loadConfig()
	tok, _ := cfg.githubToken()
	gh := newGHClient(tok)

	info, err := checkSelfUpdate(gh)
	if err != nil {
		fatalf("could not check for updates: %v", err)
	}
	if info.Latest == "" {
		fmt.Println("No published releases yet.")
		return 0
	}

	fmt.Printf("installed  %s\n", info.Current)
	fmt.Printf("latest     %s\n", info.Latest)
	if info.URL != "" {
		fmt.Printf("release    %s\n", info.URL)
	}

	if isDevBuild() {
		fmt.Println("\nThis is a local build, so there is nothing to update.")
		fmt.Println("Build a stamped one with ./build.sh, or download a release.")
		return 0
	}
	if !info.Available {
		fmt.Println("\nAlready up to date.")
		return 0
	}
	if args.check {
		fmt.Println("\nAn update is available. Run 'wowbak self-update' to install it.")
		return 1
	}

	self, err := os.Executable()
	if err != nil {
		fatalf("cannot locate the running program: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	targets := []string{self}
	if args.all {
		targets = siblingTargets(self)
	}

	updated, failed := 0, 0
	for _, t := range targets {
		name := filepath.Base(t)
		if err := checkWritable(filepath.Dir(t)); err != nil {
			fmt.Printf("  %-22s cannot write there: %v\n", name, err)
			failed++
			continue
		}
		asset, ok := assetFor(gh, info, name)
		if !ok {
			fmt.Printf("  %-22s no download published for %s/%s\n",
				name, runtime.GOOS, runtime.GOARCH)
			failed++
			continue
		}
		tmp := t + ".new"
		fmt.Printf("  %-22s downloading %s\n", name, human(asset.Size))
		if err := gh.downloadTo(asset, tmp, nil); err != nil {
			os.Remove(tmp)
			fmt.Printf("  %-22s failed: %v\n", name, err)
			failed++
			continue
		}
		if err := replaceBinary(t, tmp); err != nil {
			os.Remove(tmp)
			fmt.Printf("  %-22s failed: %v\n", name, err)
			failed++
			continue
		}
		updated++
	}

	if updated == 0 {
		fmt.Printf("\nNothing was updated.\n")
		return 1
	}
	fmt.Printf("\nupdated %d file(s) to %s", updated, info.Latest)
	if failed > 0 {
		fmt.Printf(", %d could not be updated", failed)
	}
	fmt.Println()
	fmt.Println("Restart wowbak to run the new version.")
	return 0
}

// siblingTargets lists the shipped binaries in the same folder as the running
// one. exeDir is used rather than the executable's own directory because a
// macOS app bundle puts its binary three levels down, and the other platforms'
// binaries sit beside the bundle, not beside it.
func siblingTargets(self string) []string {
	dir := exeDir()
	if dir == "" {
		dir = filepath.Dir(self)
	}
	seen := map[string]bool{self: true}
	out := []string{self}

	add := func(p string) {
		if seen[p] {
			return
		}
		if _, err := os.Stat(p); err == nil {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, name := range updatableSiblings {
		add(filepath.Join(dir, name))
	}
	// The launcher bundle's binary, which is not a flat file in the folder.
	add(filepath.Join(dir, "WowBackup.app", "Contents", "MacOS", "WowBackup"))
	return out
}

// assetNameFor maps a local filename to the release asset that replaces it.
// Names that are published verbatim map to themselves; anything else - notably
// the binary inside WowBackup.app, which is called WowBackup - is just this
// platform's build under a different name.
func assetNameFor(local string) string {
	for _, n := range updatableSiblings {
		if n == local {
			return n
		}
	}
	return releaseAssetName()
}

// assetFor finds the published file matching a local filename.
func assetFor(gh *ghClient, info *updateInfo, local string) (ghAsset, bool) {
	name := assetNameFor(local)
	if name == "" {
		return ghAsset{}, false
	}
	if info.Asset.Name == name {
		return info.Asset, true
	}
	rels, err := gh.releases(repoSlug)
	if err != nil {
		return ghAsset{}, false
	}
	for _, rel := range rels {
		if rel.TagName != info.Latest {
			continue
		}
		if a, ok := rel.asset(name); ok {
			return a, true
		}
	}
	return ghAsset{}, false
}
