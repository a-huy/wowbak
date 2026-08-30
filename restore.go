package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type planEntry struct {
	flavor  string
	rel     string
	arcname string
	action  string // "add" or "overwrite"
}

func cmdRestore(args restoreArgs) int {
	m := readManifest(args.archive)
	install := resolveInstall(args.installPath)

	flavors := args.flavor
	if len(flavors) == 0 {
		flavors = sortedFlavors(m.Flavors)
	}
	var unknown []string
	for _, f := range flavors {
		if _, ok := m.Flavors[f]; !ok {
			unknown = append(unknown, f)
		}
	}
	if len(unknown) > 0 {
		fatalf("archive has no flavor(s): %s. It contains: %s",
			strings.Join(unknown, ", "), strings.Join(sortedFlavors(m.Flavors), ", "))
	}
	if args.addonsOnly && args.wtfOnly {
		fatalf("--addons-only and --wtf-only are mutually exclusive")
	}

	// Mirroring is the "set this machine up like that one" case: every addon
	// folder is replaced outright, anything not in the backup is set aside, and
	// game versions missing here are created. Merging - the default - is for
	// bringing one machine's setup alongside what is already installed.
	if args.mirror {
		args.replaceAddons = true
		args.clean = true
		args.createMissing = true
	}

	wanted := func(rel string) bool {
		if args.addonsOnly {
			return strings.HasPrefix(rel, "Interface/AddOns/")
		}
		if args.wtfOnly {
			return strings.HasPrefix(rel, "WTF/")
		}
		return true
	}

	fmt.Printf("archive   %s\n", args.archive)
	fmt.Printf("from      %s on %s (%s)\n", m.Source.OS, dash(m.Source.Hostname), m.Source.InstallPath)
	fmt.Printf("into      %s (%s)\n", install, machineID())
	if args.mirror {
		fmt.Printf("mode      mirror - this machine will be made to match the backup\n")
		fmt.Printf("          addons not in the backup are set aside; settings for other\n")
		fmt.Printf("          accounts are left alone, as is this machine's Config.wtf\n")
	} else {
		fmt.Printf("mode      merge - the backup is added to what is already here\n")
	}
	fmt.Println()

	var plan []planEntry
	var skipped []string
	for _, flavor := range flavors {
		flavorDir := filepath.Join(install, flavor)
		if !isDir(flavorDir) && !args.createMissing {
			skipped = append(skipped, flavor)
			continue
		}
		rels := make([]string, 0, len(m.Flavors[flavor].Files))
		for r := range m.Flavors[flavor].Files {
			rels = append(rels, r)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			if !wanted(rel) {
				continue
			}
			dest := filepath.Join(flavorDir, filepath.FromSlash(rel))
			action := "add"
			if _, err := os.Stat(dest); err == nil {
				action = "overwrite"
			}
			plan = append(plan, planEntry{flavor, rel, payloadRoot + "/" + flavor + "/" + rel, action})
		}
	}
	for _, f := range skipped {
		fmt.Printf("skipping %s: not installed here (--create-missing to create it)\n", f)
	}
	if len(plan) == 0 {
		fmt.Println("nothing to restore.")
		return 0
	}

	// --replace-addons: an addon folder must end up exactly as the archive has
	// it. Merging leaves files from a newer version sitting beside an older
	// .toc, which is how a rolled-back addon ends up in a state that never
	// shipped. WTF is never touched this way; settings are always merged.
	replaceRoots := map[string]bool{} // "<flavor>/Interface/AddOns/<Name>"
	if args.replaceAddons {
		for _, p := range plan {
			parts := strings.Split(p.rel, "/")
			if len(parts) > 3 && strings.HasPrefix(p.rel, "Interface/AddOns/") {
				replaceRoots[p.flavor+"/Interface/AddOns/"+parts[2]] = true
			}
		}
	}

	adds := 0
	for _, p := range plan {
		if p.action == "add" {
			adds++
		}
	}
	overwrites := len(plan) - adds
	fmt.Printf("%d new file(s), %d would be overwritten\n", adds, overwrites)

	// Fail before touching anything if the install is not writable by this user.
	// A WoW install under Program Files with default ACLs needs elevation, and finding
	// that out halfway through a restore would leave a half-applied setup.
	checked := map[string]bool{}
	for _, p := range plan {
		if checked[p.flavor] {
			continue
		}
		checked[p.flavor] = true
		dir := filepath.Join(install, p.flavor)
		if !isDir(dir) {
			continue
		}
		if err := checkWritable(dir); err != nil {
			fatalf("cannot write to %s: %v\n"+
				"Run wowbak from an account that owns the WoW folder, or move the\n"+
				"install somewhere user-writable. No files were changed.", dir, err)
		}
	}

	if args.dryRun {
		if len(replaceRoots) > 0 {
			roots := make([]string, 0, len(replaceRoots))
			for r := range replaceRoots {
				roots = append(roots, r)
			}
			sort.Strings(roots)
			fmt.Printf("%d addon folder(s) would be replaced outright:\n", len(roots))
			for _, r := range roots {
				fmt.Printf("  = %s\n", r)
			}
		}
		for i, p := range plan {
			if i == 40 {
				fmt.Printf("  ... and %d more\n", len(plan)-40)
				break
			}
			mark := "+"
			if p.action == "overwrite" {
				mark = "~"
			}
			fmt.Printf("  %s %s/%s\n", mark, p.flavor, p.rel)
		}
		fmt.Println("\ndry run: nothing written.")
		return 0
	}

	if overwrites > 0 && !args.force {
		fatalf("%d existing file(s) would be overwritten. "+
			"Re-run with --force (a safety archive is written first), or --dry-run to review.",
			overwrites)
	}

	// Everything inside a replaced folder is about to go, not just the files the
	// archive happens to overwrite, so the snapshot has to cover all of it.
	extra := extraFilesUnder(install, replaceRoots, plan)
	if (overwrites > 0 || len(extra) > 0) && !args.noSafety {
		// The snapshot holds THIS machine's files, so it belongs in this machine's
		// backup folder, not next to the archive it is being replaced from.
		safetyDir := loadConfig().backupDir()
		if err := os.MkdirAll(safetyDir, 0o755); err != nil {
			safetyDir, _ = filepath.Abs(filepath.Dir(args.archive))
		}
		safety := filepath.Join(safetyDir, time.Now().Format("wowbak-pre-restore-20060102-150405.zip"))
		fmt.Printf("saving replaced files to %s\n", safety)
		if err := writeSafety(safety, install, append(append([]planEntry{}, plan...), extra...)); err != nil {
			fatalf("could not write safety archive: %v\nNo files were changed.", err)
		}
	}

	// Move the folders aside rather than deleting: if extraction fails we can
	// put them straight back.
	var aside [][2]string
	restoreAside := func() {
		for _, a := range aside {
			os.RemoveAll(a[0])
			os.Rename(a[1], a[0])
		}
	}
	for root := range replaceRoots {
		flavor, rel, _ := strings.Cut(root, "/")
		live := filepath.Join(install, flavor, filepath.FromSlash(rel))
		if !isDir(live) {
			continue
		}
		tmp := live + ".wowbak-replacing"
		os.RemoveAll(tmp)
		if err := os.Rename(live, tmp); err != nil {
			restoreAside()
			fatalf("could not replace %s: %v\nNo files were changed.", rel, err)
		}
		aside = append(aside, [2]string{live, tmp})
	}

	zr, err := zip.OpenReader(args.archive)
	if err != nil {
		fatalf("cannot open %s: %v", args.archive, err)
	}
	defer zr.Close()
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}

	written := 0
	for _, p := range plan {
		src, ok := byName[p.arcname]
		if !ok {
			fatalf("archive is missing %s (corrupt?). %d file(s) already written.", p.arcname, written)
		}
		dest := filepath.Join(install, p.flavor, filepath.FromSlash(p.rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			fatalf("cannot create %s: %v", filepath.Dir(dest), err)
		}
		if err := extractTo(src, dest); err != nil {
			restoreAside()
			fatalf("cannot write %s: %v\nPut back what was moved aside; %d file(s) had been written.",
				dest, err, written)
		}
		written++
		if written%500 == 0 {
			fmt.Printf("  %d/%d\r", written, len(plan))
		}
	}
	for _, a := range aside {
		os.RemoveAll(a[1])
	}
	fmt.Printf("  %d/%d files          \n", written, len(plan))
	if len(replaceRoots) > 0 {
		fmt.Printf("  %d addon folder(s) replaced outright\n", len(replaceRoots))
	}

	if args.clean {
		cleanStale(install, flavors, plan)
	}

	fmt.Printf("\nrestored into %s\n", install)
	fmt.Println("Note: WTF/Config.wtf is excluded by default, so graphics settings stay machine-local.")
	return 0
}

func extractTo(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// writeSafety snapshots the files a restore is about to replace. It writes a full
// manifest too, so the snapshot is itself a wowbak archive: rolling a restore back
// is just "wowbak restore <the-snapshot> --force".
func writeSafety(path, install string, plan []planEntry) error {
	f, err := os.Create(path)
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
		Source: Source{
			OS: osLabel(), Platform: platformLabel(), Hostname: host, InstallPath: install,
		},
		Note:    "pre-restore snapshot: the files replaced by a restore, as they were",
		Flavors: map[string]*FlavorData{},
	}

	for _, p := range plan {
		if p.action != "overwrite" {
			continue
		}
		src := filepath.Join(install, p.flavor, filepath.FromSlash(p.rel))
		st, err := os.Stat(src)
		if err != nil {
			zw.Close()
			return err
		}
		sum, err := sha256File(src)
		if err != nil {
			zw.Close()
			return err
		}
		if m.Flavors[p.flavor] == nil {
			m.Flavors[p.flavor] = &FlavorData{Files: map[string]FileMeta{}}
		}
		m.Flavors[p.flavor].Files[p.rel] = FileMeta{
			Size: st.Size(), MTime: st.ModTime().Unix(), SHA256: sum,
		}
		if err := addFile(zw, src, p.arcname); err != nil {
			zw.Close()
			return err
		}
	}

	for flavor, d := range m.Flavors {
		var bytes int64
		for _, meta := range d.Files {
			bytes += meta.Size
		}
		d.Addons = summarizeAddons(filepath.Join(install, flavor), d.Files)
		d.WTF = summarizeWTF(d.Files)
		d.Totals = Totals{Files: len(d.Files), Bytes: bytes}
	}

	data, _ := json.MarshalIndent(m, "", "  ")
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: manifestName, Method: zip.Deflate})
	if err != nil {
		zw.Close()
		return err
	}
	if _, err := mw.Write(data); err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

// cleanStale sets aside addon dirs that are not in the archive, renaming rather
// than deleting so nothing is unrecoverable.
func cleanStale(install string, flavors []string, plan []planEntry) {
	keep := map[string]map[string]bool{}
	for _, f := range flavors {
		keep[f] = map[string]bool{}
	}
	for _, p := range plan {
		parts := strings.Split(p.rel, "/")
		if len(parts) > 3 && strings.HasPrefix(p.rel, "Interface/AddOns/") {
			keep[p.flavor][parts[2]] = true
		}
	}
	for flavor, names := range keep {
		if len(names) == 0 {
			continue // nothing restored for this flavor; do not touch it
		}
		addonRoot := filepath.Join(install, flavor, "Interface", "AddOns")
		entries, err := os.ReadDir(addonRoot)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			target := filepath.Join(addonRoot, name)
			st, err := os.Lstat(target)
			if err != nil || names[name] || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				continue
			}
			stale := target + ".wowbak-removed"
			if err := os.Rename(target, stale); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not set aside %s/%s: %v\n", flavor, name, err)
				continue
			}
			fmt.Printf("  removed addon %s/%s (renamed to %s)\n", flavor, name, filepath.Base(stale))
		}
	}
}

// extraFilesUnder lists files living inside folders that are about to be
// replaced but which the archive does not itself contain - the leftovers that
// make a merged restore produce a version that never existed. They are returned
// as plan entries purely so the safety snapshot can preserve them.
func extraFilesUnder(install string, roots map[string]bool, plan []planEntry) []planEntry {
	if len(roots) == 0 {
		return nil
	}
	planned := map[string]bool{}
	for _, p := range plan {
		planned[p.flavor+"/"+p.rel] = true
	}
	var out []planEntry
	for root := range roots {
		flavor, rel, _ := strings.Cut(root, "/")
		base := filepath.Join(install, flavor, filepath.FromSlash(rel))
		if !isDir(base) {
			continue
		}
		filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			relOS, err := filepath.Rel(filepath.Join(install, flavor), p)
			if err != nil {
				return nil
			}
			r := filepath.ToSlash(relOS)
			if planned[flavor+"/"+r] {
				return nil
			}
			out = append(out, planEntry{flavor, r, payloadRoot + "/" + flavor + "/" + r, "overwrite"})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}
