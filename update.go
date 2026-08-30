package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// checkResult is what we learned about one package.
type checkResult struct {
	Pkg       Package
	Source    source
	Latest    string
	Asset     ghAsset
	Flavor    string // release flavor the asset was matched on
	Release   ghRelease
	Outdated  bool
	Reason    string // why it could not be checked, when it could not be
	Untracked bool   // no source configured
}

func checkOne(gh *ghClient, p Package, src source) checkResult {
	res := checkResult{Pkg: p, Source: src}

	rels, err := gh.releases(src.Repo)
	if err != nil {
		res.Reason = err.Error()
		return res
	}

	// Walk newest first and take the first release that has a build for this
	// game version. A release may be Classic-only, which must not match Retail.
	for _, rel := range rels {
		if rel.Draft || rel.Prerelease {
			continue
		}
		var man *releaseManifest
		if a, ok := rel.asset("release.json"); ok {
			if m, err := gh.manifest(a); err == nil {
				man = m
			}
		}
		asset, flavor, ok := pickAsset(rel, man, p.Interface)
		if !ok {
			continue
		}
		res.Release, res.Asset, res.Flavor = rel, asset, flavor
		res.Latest = strings.TrimPrefix(rel.TagName, "v")
		res.Outdated = newerThan(res.Latest, p.Version)
		return res
	}
	res.Reason = "no release with a build for this game version"
	return res
}

// flavorCheck is one game version's worth of results.
type flavorCheck struct {
	Flavor  string
	Results []checkResult
}

// runCheck is the shared check used by both the command line and the interface,
// so the two can never disagree about what is outdated.
func runCheck(cfg Config, install string, flavors []string, only map[string]bool,
	progress func(string)) ([]flavorCheck, *ghClient) {
	tok, _ := cfg.githubToken()
	gh := newGHClient(tok)
	var out []flavorCheck
	for _, flavor := range flavors {
		pkgs := scanPackages(install, flavor)
		tracked, untracked := groupBySource(cfg, pkgs)

		var results []checkResult
		for _, g := range tracked {
			if only != nil && !g.matches(only) {
				continue
			}
			if progress != nil {
				progress(g.pkg.Name)
			}
			results = append(results, checkOne(gh, g.pkg, g.src))
		}
		for _, p := range untracked {
			if only != nil && !only[strings.ToLower(p.Name)] {
				continue
			}
			results = append(results, checkResult{Pkg: p, Untracked: true})
		}
		sort.Slice(results, func(i, j int) bool {
			return strings.ToLower(results[i].Pkg.Name) < strings.ToLower(results[j].Pkg.Name)
		})
		out = append(out, flavorCheck{Flavor: flavor, Results: results})
	}
	return out, gh
}

// sourceGroup is every folder that comes from one download.
type sourceGroup struct {
	src     source
	pkg     Package // merged: all folders, version taken from the main one
	members []string
}

func (g sourceGroup) matches(only map[string]bool) bool {
	for _, m := range g.members {
		if only[strings.ToLower(m)] {
			return true
		}
	}
	return false
}

// groupBySource merges packages that resolve to the same source into one unit.
//
// This matters because add-ons that ship several folders version them
// independently: Narcissus 1.8.6 installs Narcissus_BagFilter 1.0.2, and that
// sub-folder's version never catches up with the release tag. Checked
// separately it would report as outdated forever and re-download the whole
// package - 87MB in that case - on every run. The version that counts is the
// one on the folder the package is named after.
func groupBySource(cfg Config, pkgs []Package) ([]sourceGroup, []Package) {
	order := []string{}
	groups := map[string]*sourceGroup{}
	var untracked []Package

	for _, p := range pkgs {
		src, ok := cfg.sourceFor(p.Name)
		if !ok {
			untracked = append(untracked, p)
			continue
		}
		key := src.String()
		g := groups[key]
		if g == nil {
			g = &sourceGroup{src: src, pkg: p}
			groups[key] = g
			order = append(order, key)
		}
		g.members = append(g.members, p.Name)
		g.pkg.Folders = append(g.pkg.Folders, p.Folders...)
		// Prefer the folder whose name matches the repository, then the
		// shortest - "Narcissus" over "Narcissus_BagFilter".
		if betterPrimary(p, g.pkg, src) {
			g.pkg.Name, g.pkg.Version, g.pkg.Interface = p.Name, p.Version, p.Interface
		}
	}

	out := make([]sourceGroup, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.Strings(g.pkg.Folders)
		sort.Strings(g.members)
		out = append(out, *g)
	}
	return out, untracked
}

func betterPrimary(candidate, current Package, src source) bool {
	repo := src.Repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	if strings.EqualFold(candidate.Name, repo) && !strings.EqualFold(current.Name, repo) {
		return true
	}
	if strings.EqualFold(current.Name, repo) {
		return false
	}
	return len(candidate.Name) < len(current.Name)
}

func cmdOutdated(args updateArgs) int {
	cfg := loadConfig()
	install := resolveInstall(args.installPath)
	flavors := resolveFlavors(install, args.flavor)

	checks, gh := runCheck(cfg, install, flavors, nil, nil)

	total, outdated, untracked, failed := 0, 0, 0, 0
	for _, fc := range checks {
		flavor, results := fc.Flavor, fc.Results
		if len(results) == 0 {
			continue
		}
		fmt.Printf("%s\n\n", flavor)
		for _, r := range results {
			total++
			switch {
			case r.Untracked:
				untracked++
				if args.all {
					fmt.Printf("  ? %-28s %-14s no source configured\n", r.Pkg.Name, dash(r.Pkg.Version))
				}
			case r.Reason != "":
				failed++
				fmt.Printf("  ! %-28s %-14s %s\n", r.Pkg.Name, dash(r.Pkg.Version), r.Reason)
			case r.Outdated:
				outdated++
				fmt.Printf("  ~ %-28s %-14s -> %-14s %s\n",
					r.Pkg.Name, dash(r.Pkg.Version), r.Latest, r.Source)
			case args.all:
				fmt.Printf("  = %-28s %-14s up to date\n", r.Pkg.Name, dash(r.Pkg.Version))
			}
		}
		fmt.Println()
	}

	fmt.Printf("%d outdated, %d checked, %d without a source", outdated, total-untracked-failed, untracked)
	if failed > 0 {
		fmt.Printf(", %d could not be checked", failed)
	}
	fmt.Println(".")
	if !args.all && untracked > 0 {
		fmt.Println("Run with --all to list them, or 'wowbak sources --discover' to find their repos.")
	}
	if note := gh.rateNote(); note != "" {
		fmt.Println(note)
	}
	if outdated > 0 {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- applying

func cmdUpdate(args updateArgs) int {
	cfg := loadConfig()
	install := resolveInstall(args.installPath)
	flavors := resolveFlavors(install, args.flavor)

	var only map[string]bool
	if len(args.names) > 0 {
		only = map[string]bool{}
		for _, n := range args.names {
			only[strings.ToLower(n)] = true
		}
	} else if !args.all {
		fatalf("name the addons to update, or pass --all")
	}

	checks, gh := runCheck(cfg, install, flavors, only, nil)

	updated, failed := 0, 0
	for _, fc := range checks {
		flavor, results := fc.Flavor, fc.Results

		var todo []checkResult
		for _, r := range results {
			if r.Outdated && r.Asset.URL != "" {
				todo = append(todo, r)
			}
		}
		if len(todo) == 0 {
			continue
		}

		fmt.Printf("%s - %d to update\n\n", flavor, len(todo))
		for _, r := range todo {
			fmt.Printf("  %s  %s -> %s", r.Pkg.Name, dash(r.Pkg.Version), r.Latest)
			if r.Flavor != "" {
				fmt.Printf("  [%s]", r.Flavor)
			}
			fmt.Println()
			if args.dryRun {
				fmt.Printf("    would replace: %s\n", strings.Join(r.Pkg.Folders, ", "))
				continue
			}
			if err := applyUpdate(gh, cfg, install, flavor, r); err != nil {
				failed++
				fmt.Printf("    failed: %v\n", err)
				continue
			}
			updated++
		}
		fmt.Println()
	}

	if args.dryRun {
		fmt.Println("dry run: nothing was changed.")
		return 0
	}
	fmt.Printf("%d updated", updated)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println(".")
	if note := gh.rateNote(); note != "" {
		fmt.Println(note)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// applyUpdate downloads one package and swaps its folders into place. The old
// folders are snapshotted first and only deleted once the new ones are in, so a
// failure at any point leaves the install as it was.
func applyUpdate(gh *ghClient, cfg Config, install, flavor string, r checkResult) error {
	tmp, err := os.CreateTemp("", "wowbak-*.zip")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	shown := int64(-1)
	err = gh.downloadTo(r.Asset, tmpPath, func(done, total int64) {
		if total < 8<<20 {
			return // too quick to be worth reporting
		}
		if pct := done * 100 / total; pct/5 != shown {
			shown = pct / 5
			fmt.Printf("    downloading %s  %d%%\r", human(total), pct)
		}
	})
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if r.Asset.Size >= 8<<20 {
		fmt.Printf("    downloaded %s              \n", human(r.Asset.Size))
	}

	zc, err := zip.OpenReader(tmpPath)
	if err != nil {
		return fmt.Errorf("unreadable archive: %w", err)
	}
	defer zc.Close()
	zr := &zc.Reader

	// The archive's top-level directories are the folders to install. This is
	// the authoritative folder list - better than any guess made from names.
	incoming := map[string]bool{}
	for _, f := range zr.File {
		top, _, ok := strings.Cut(path.Clean(f.Name), "/")
		if !ok || top == "" || top == "." || strings.HasPrefix(top, "..") {
			continue
		}
		incoming[top] = true
	}
	if len(incoming) == 0 {
		return fmt.Errorf("archive contains no addon folders")
	}
	var folders []string
	for f := range incoming {
		folders = append(folders, f)
	}
	sort.Strings(folders)

	addonRoot := filepath.Join(install, flavor, "Interface", "AddOns")
	if err := checkWritable(addonRoot); err != nil {
		return fmt.Errorf("cannot write to AddOns: %w", err)
	}

	// Snapshot whatever we are about to overwrite, as a restorable archive.
	snapDir := cfg.backupDir()
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", snapDir, err)
	}
	snap := filepath.Join(snapDir,
		fmt.Sprintf("wowbak-pre-update-%s-%s.zip", safeName(r.Pkg.Name),
			time.Now().Format("20060102-150405")))
	existing := folders
	if err := snapshotFolders(install, flavor, existing, snap); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	// Extract into a staging folder first, so a corrupt archive never leaves a
	// half-written addon behind.
	stage := filepath.Join(addonRoot, ".wowbak-staging")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := extractZip(zr, stage); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Swap folder by folder, keeping the old copy until the new one is in place.
	var movedAside [][2]string
	rollback := func() {
		for _, m := range movedAside {
			os.RemoveAll(m[0])
			os.Rename(m[1], m[0])
		}
	}
	for _, f := range folders {
		live := filepath.Join(addonRoot, f)
		staged := filepath.Join(stage, f)
		if _, err := os.Stat(staged); err != nil {
			continue
		}
		aside := live + ".wowbak-replacing"
		os.RemoveAll(aside)
		if _, err := os.Stat(live); err == nil {
			if err := os.Rename(live, aside); err != nil {
				rollback()
				return fmt.Errorf("could not set aside %s: %w", f, err)
			}
			movedAside = append(movedAside, [2]string{live, aside})
		}
		if err := os.Rename(staged, live); err != nil {
			rollback()
			return fmt.Errorf("could not install %s: %w", f, err)
		}
	}
	for _, m := range movedAside {
		os.RemoveAll(m[1])
	}

	fmt.Printf("    %d folder(s) replaced\n", len(folders))
	fmt.Printf("    undo: wowbak restore %s --force --replace-addons\n",
		filepath.Join(filepath.Base(filepath.Dir(snap)), filepath.Base(snap)))
	return nil
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, s)
}

// extractZip writes an addon archive into dir, refusing entries that would
// escape it.
func extractZip(zr *zip.Reader, dir string) error {
	for _, f := range zr.File {
		clean := path.Clean(f.Name)
		if clean == "." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") {
			continue
		}
		dest := filepath.Join(dir, filepath.FromSlash(clean))
		rel, err := filepath.Rel(dir, dest)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("archive entry escapes the target: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// snapshotFolders writes the current contents of some addon folders as a wowbak
// archive, so "wowbak restore <snapshot>" undoes an update.
func snapshotFolders(install, flavor string, folders []string, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	host, _ := os.Hostname()
	m := &Manifest{
		ManifestVersion: manifestVersion,
		Tool:            "wowbak",
		CreatedUTC:      time.Now().UTC().Format(time.RFC3339),
		Source: Source{OS: osLabel(), Platform: platformLabel(), Hostname: host,
			InstallPath: install},
		Note:    "pre-update snapshot: these addon folders as they were before updating",
		Flavors: map[string]*FlavorData{},
	}
	fd := &FlavorData{Files: map[string]FileMeta{}}

	for _, folder := range folders {
		base := filepath.Join(install, flavor, "Interface", "AddOns", folder)
		if !isDir(base) {
			continue // a folder the new version adds; nothing to preserve
		}
		err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			relOS, err := filepath.Rel(filepath.Join(install, flavor), p)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(relOS)
			sum, err := sha256File(p)
			if err != nil {
				return err
			}
			fd.Files[rel] = FileMeta{Size: info.Size(), MTime: info.ModTime().Unix(), SHA256: sum}
			return addFile(zw, p, payloadRoot+"/"+flavor+"/"+rel)
		})
		if err != nil {
			zw.Close()
			return err
		}
	}

	var total int64
	for _, meta := range fd.Files {
		total += meta.Size
	}
	fd.Addons = summarizeAddons(filepath.Join(install, flavor), fd.Files)
	fd.WTF = summarizeWTF(fd.Files)
	fd.Totals = Totals{Files: len(fd.Files), Bytes: total}
	m.Flavors[flavor] = fd

	data, _ := json.MarshalIndent(m, "", "  ")
	w, err := zw.CreateHeader(&zip.FileHeader{Name: manifestName, Method: zip.Deflate})
	if err != nil {
		zw.Close()
		return err
	}
	if _, err := w.Write(data); err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}
