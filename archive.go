package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

func osLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	default:
		if runtime.GOOS == "" {
			return "unknown"
		}
		return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
	}
}

func platformLabel() string { return runtime.GOOS + "/" + runtime.GOARCH }

func readManifestQuiet(archive string) *Manifest {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return nil
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != manifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		var m Manifest
		if err := json.NewDecoder(rc).Decode(&m); err != nil {
			return nil
		}
		return &m
	}
	return nil
}

func readManifest(archive string) *Manifest {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		fatalf("cannot read %s: %v", archive, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != manifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			fatalf("cannot read manifest in %s: %v", archive, err)
		}
		defer rc.Close()
		var m Manifest
		if err := json.NewDecoder(rc).Decode(&m); err != nil {
			fatalf("corrupt manifest in %s: %v", archive, err)
		}
		return &m
	}
	fatalf("%s has no %s - not a wowbak archive", archive, manifestName)
	return nil
}

// checkWritable verifies we can actually create files under dir before promising to.
// On Windows this is what catches a WoW install under Program Files with default ACLs,
// where writing needs elevation.
func checkWritable(dir string) error {
	probe := filepath.Join(dir, ".wowbak-write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(probe)
}

func cmdBackup(args backupArgs) int {
	cfg := loadConfig()
	install := resolveInstall(args.installPath)
	registerMachine(cfg, install)
	flavors := resolveFlavors(install, args.flavor)
	excludes := cfg.excludesOrDefault()
	if args.includeConfig {
		var kept []string
		for _, p := range excludes {
			if !strings.HasSuffix(p, "Config.wtf") {
				kept = append(kept, p)
			}
		}
		excludes = kept
	}

	out := args.output
	if out == "" {
		dir := cfg.backupDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatalf("cannot create backup folder %s: %v", dir, err)
		}
		out = filepath.Join(dir, time.Now().Format("wowbak-20060102-150405.zip"))
	}
	out, _ = filepath.Abs(expandHome(out))
	if _, err := os.Stat(out); err == nil && !args.force {
		fatalf("%s already exists (use --force to overwrite)", out)
	}

	fmt.Printf("scanning %s ...\n", install)
	m, warns := buildManifest(install, flavors, excludes, args.followSymlinks)
	total := 0
	for _, d := range m.Flavors {
		total += d.Totals.Files
	}
	fmt.Printf("writing %d files to %s\n", total, out)

	if dir := filepath.Dir(out); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	tmp := out + ".partial"
	written, err := writeArchive(tmp, install, m, &warns)
	if err != nil {
		os.Remove(tmp)
		fatalf("%v", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		fatalf("cannot finalize %s: %v", out, err)
	}

	st, _ := os.Stat(out)
	fmt.Printf("  %d/%d files          \n", written, total)
	fmt.Printf("\ndone: %s (%s)\n", out, human(st.Size()))
	printWarnings(warns)
	return 0
}

func writeArchive(path, install string, m *Manifest, warns *[]string) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("cannot create %s: %w", path, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	data, _ := json.MarshalIndent(m, "", "  ")
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: manifestName, Method: zip.Deflate})
	if err != nil {
		return 0, err
	}
	if _, err := mw.Write(data); err != nil {
		return 0, err
	}

	total := 0
	for _, d := range m.Flavors {
		total += d.Totals.Files
	}

	written := 0
	for _, flavor := range sortedFlavors(m.Flavors) {
		rels := make([]string, 0, len(m.Flavors[flavor].Files))
		for r := range m.Flavors[flavor].Files {
			rels = append(rels, r)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			src := filepath.Join(install, flavor, filepath.FromSlash(rel))
			if err := addFile(zw, src, payloadRoot+"/"+flavor+"/"+rel); err != nil {
				*warns = append(*warns, fmt.Sprintf("failed to archive %s/%s: %v", flavor, rel, err))
				continue
			}
			written++
			if written%500 == 0 {
				fmt.Printf("  %d/%d\r", written, total)
			}
		}
	}
	if err := zw.Close(); err != nil {
		return written, err
	}
	return written, f.Sync()
}

func addFile(zw *zip.Writer, src, arcname string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	hdr := &zip.FileHeader{Name: arcname, Method: zip.Deflate}
	hdr.SetModTime(st.ModTime())
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

func cmdScan(args scanArgs) int {
	install := resolveInstall(args.installPath)
	flavors := resolveFlavors(install, args.flavor)
	m, warns := buildManifest(install, flavors, loadConfig().excludesOrDefault(), args.followSymlinks)
	printManifest(m, args.addons)
	printWarnings(warns)
	return 0
}

func cmdInspect(args inspectArgs) int {
	printManifest(readManifest(args.archive), args.addons)
	return 0
}

func cmdConfig(args configArgs) int {
	cfg := loadConfig()

	switch args.action {
	case "set":
		if args.key != "install_path" {
			fatalf("only install_path is settable here; edit %s for anything else", confName)
		}
		abs, err := filepath.Abs(expandHome(args.value))
		if err != nil || !isDir(abs) {
			fatalf("not a directory: %s", args.value)
		}
		if !looksLikeInstall(abs) {
			found := presentFlavors(abs)
			list := "nothing"
			if len(found) > 0 {
				list = strings.Join(found, ", ")
			}
			fatalf("%s contains no game version dirs. Point at the folder holding "+
				"_retail_ / _classic_era_. (found: %s)", abs, list)
		}
		target := cfg.Path
		if target == "" {
			target = portableConfPath()
			if target == "" {
				target = userConfPath()
			}
			if !fileExists(target) {
				os.MkdirAll(filepath.Dir(target), 0o755)
				os.WriteFile(target, []byte(confTemplate), 0o644)
			}
		}
		// On a portable config, tag the key with the OS: the same stick is
		// expected to see a different install path on each machine.
		key := "install_path"
		if target == portableConfPath() {
			key = "install_path." + machineID()
		}
		if err := setConfValue(target, key, abs); err != nil {
			fatalf("cannot update %s: %v", target, err)
		}
		fmt.Printf("%s = %s\nsaved to %s\n", key, abs, target)
		return 0
	case "unset":
		if cfg.Path == "" {
			fatalf("no config file to edit")
		}
		for _, k := range []string{args.key, args.key + "." + machineID()} {
			setConfValue(cfg.Path, k, "")
		}
		fmt.Printf("cleared %s in %s\n", args.key, cfg.Path)
		return 0
	}

	if cfg.Path == "" {
		fmt.Printf("config file   none found; looked in:\n")
		for _, p := range configSearch() {
			fmt.Printf("                %s\n", p)
		}
		fmt.Printf("              run \"wowbak config init\" to create one\n")
	} else {
		kind := "user"
		if cfg.Portable {
			kind = "portable, next to the binary"
		}
		fmt.Printf("config file   %s  (%s)\n", cfg.Path, kind)
		if cfg.InstallPath != "" {
			fmt.Printf("  install_path: %s\n", cfg.InstallPath)
		} else {
			fmt.Printf("  install_path: (unset, autodetecting)\n")
		}
	}
	fmt.Printf("machine       %s%s\n", machineID(), map[bool]string{
		true:  "",
		false: "   (new - will be registered on first backup)",
	}[cfg.MachineKnown])
	if tok, src := cfg.githubToken(); tok != "" {
		fmt.Printf("github token  %s  (from %s)\n", maskToken(tok), src)
	} else {
		fmt.Printf("github token  not set - addon checks limited to 60 requests/hour\n")
	}
	fmt.Printf("backup folder %s\n", cfg.backupDir())
	if env := os.Getenv("WOWBAK_INSTALL_PATH"); env != "" {
		fmt.Printf("\nWOWBAK_INSTALL_PATH=%s  (overrides config)\n", env)
	}

	fmt.Printf("\nresolved:\n")
	install := resolveInstall(args.installPath)
	fmt.Printf("  install_path  %s\n", install)
	for _, f := range presentFlavors(install) {
		addonDir := filepath.Join(install, f, "Interface", "AddOns")
		count := 0
		if entries, err := os.ReadDir(addonDir); err == nil {
			count = len(entries)
		}
		wtf := "no WTF"
		if isDir(filepath.Join(install, f, "WTF")) {
			wtf = "WTF present"
		}
		writable := "writable"
		if err := checkWritable(filepath.Join(install, f)); err != nil {
			writable = "NOT WRITABLE - restore here would fail"
		}
		fmt.Printf("  %-20s %d addon dirs, %s, %s\n", f, count, wtf, writable)
	}
	return 0
}
