package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Sits alongside the real account folders under WTF/Account but is not one.
var nonAccountDirs = map[string]bool{"SavedVariables": true}

func matchOne(pat, rel, name string) bool {
	if t, ok := strings.CutPrefix(pat, "**/"); ok {
		m, _ := path.Match(t, name)
		return m
	}
	if t, ok := strings.CutPrefix(pat, "*/"); ok {
		m, _ := path.Match(t, name)
		return m
	}
	if strings.Contains(pat, "/") {
		m, _ := path.Match(pat, rel)
		return m
	}
	m, _ := path.Match(pat, name)
	return m
}

func excluded(rel, name string, patterns []string) bool {
	for _, p := range patterns {
		if matchOne(p, rel, name) {
			return true
		}
	}
	return false
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
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

// treeDigest is an order-independent fingerprint of a file set, for cheap
// "did anything in this addon change" checks.
func treeDigest(entries map[string]FileMeta) string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", k, entries[k].Size, entries[k].SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type scanner struct {
	flavorDir string
	flavor    string
	excludes  []string
	follow    bool
	files     map[string]FileMeta
	visited   map[string]bool // realpaths, so followed symlinks cannot loop
	warns     *[]string
}

func (s *scanner) warnf(format string, args ...any) {
	*s.warns = append(*s.warns, fmt.Sprintf(format, args...))
}

func (s *scanner) record(rel, abs string, info os.FileInfo) {
	sum, err := sha256File(abs)
	if err != nil {
		s.warnf("unreadable %s/%s: %v", s.flavor, rel, err)
		return
	}
	s.files[rel] = FileMeta{Size: info.Size(), MTime: info.ModTime().Unix(), SHA256: sum}
}

func (s *scanner) walk(absDir, relDir string) {
	entries, err := os.ReadDir(absDir) // already sorted by name
	if err != nil {
		s.warnf("cannot read %s/%s: %v", s.flavor, relDir, err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		rel := name
		if relDir != "" {
			rel = relDir + "/" + name
		}
		abs := filepath.Join(absDir, name)
		if excluded(rel, name, s.excludes) {
			continue
		}

		info, err := e.Info()
		if err != nil {
			s.warnf("cannot stat %s/%s: %v", s.flavor, rel, err)
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if !s.follow {
				s.warnf("skipped symlink %s/%s (--follow-symlinks to include it)", s.flavor, rel)
				continue
			}
			target, err := os.Stat(abs) // follows the link
			if err != nil {
				s.warnf("broken symlink %s/%s: %v", s.flavor, rel, err)
				continue
			}
			if target.IsDir() {
				real, err := filepath.EvalSymlinks(abs)
				if err != nil {
					s.warnf("cannot resolve %s/%s: %v", s.flavor, rel, err)
					continue
				}
				if s.visited[real] {
					s.warnf("symlink loop, skipped %s/%s", s.flavor, rel)
					continue
				}
				s.visited[real] = true
				s.walk(abs, rel)
				continue
			}
			s.record(rel, abs, target)
			continue
		}

		if e.IsDir() {
			s.walk(abs, rel)
			continue
		}
		s.record(rel, abs, info)
	}
}

func scanFlavor(install, flavor string, excludes []string, follow bool, warns *[]string) *FlavorData {
	flavorDir := filepath.Join(install, flavor)
	s := &scanner{
		flavorDir: flavorDir, flavor: flavor, excludes: excludes, follow: follow,
		files: map[string]FileMeta{}, visited: map[string]bool{}, warns: warns,
	}
	for _, root := range scanRoots {
		abs := filepath.Join(flavorDir, filepath.FromSlash(root))
		if isDir(abs) {
			s.walk(abs, root)
		}
	}

	var totalBytes int64
	for _, m := range s.files {
		totalBytes += m.Size
	}
	return &FlavorData{
		Addons: summarizeAddons(flavorDir, s.files),
		WTF:    summarizeWTF(s.files),
		Files:  s.files,
		Totals: Totals{Files: len(s.files), Bytes: totalBytes},
	}
}

// readTOC pulls Version/Interface/Title out of an addon's .toc. Best effort.
func readTOC(addonDir, addonName string) map[string]string {
	entries, err := os.ReadDir(addonDir)
	if err != nil {
		return nil
	}
	var tocs []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".toc") {
			tocs = append(tocs, e.Name())
		}
	}
	sort.Strings(tocs)
	// Prefer <AddonName>.toc over flavor-suffixed variants like <Name>_Mainline.toc.
	sort.SliceStable(tocs, func(i, j int) bool {
		return strings.EqualFold(tocs[i], addonName+".toc")
	})

	for _, name := range tocs {
		data, err := os.ReadFile(filepath.Join(addonDir, name))
		if err != nil {
			continue
		}
		fields := map[string]string{}
		for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "##") {
				continue
			}
			key, value, found := strings.Cut(line[2:], ":")
			if found {
				fields[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
			}
		}
		out := map[string]string{}
		for _, k := range []string{"version", "interface", "title"} {
			if v := fields[k]; v != "" {
				out[k] = v
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func summarizeAddons(flavorDir string, files map[string]FileMeta) map[string]AddonMeta {
	const prefix = "Interface/AddOns/"
	grouped := map[string]map[string]FileMeta{}
	for rel, meta := range files {
		if !strings.HasPrefix(rel, prefix) {
			continue
		}
		remainder := rel[len(prefix):]
		name, _, found := strings.Cut(remainder, "/")
		if !found {
			continue // loose file directly in AddOns/
		}
		if grouped[name] == nil {
			grouped[name] = map[string]FileMeta{}
		}
		grouped[name][rel] = meta
	}

	addons := map[string]AddonMeta{}
	for name, entries := range grouped {
		var bytes int64
		for _, e := range entries {
			bytes += e.Size
		}
		toc := readTOC(filepath.Join(flavorDir, "Interface", "AddOns", name), name)
		addons[name] = AddonMeta{
			Version:   toc["version"],
			Interface: toc["interface"],
			Title:     toc["title"],
			Files:     len(entries),
			Bytes:     bytes,
			Digest:    treeDigest(entries),
		}
	}
	return addons
}

func summarizeWTF(files map[string]FileMeta) WTFMeta {
	entries := map[string]FileMeta{}
	accounts := map[string]bool{}
	var bytes int64
	for rel, meta := range files {
		if !strings.HasPrefix(rel, "WTF/") {
			continue
		}
		entries[rel] = meta
		bytes += meta.Size
		parts := strings.Split(rel, "/")
		if len(parts) > 2 && parts[1] == "Account" && !nonAccountDirs[parts[2]] {
			accounts[parts[2]] = true
		}
	}
	names := make([]string, 0, len(accounts))
	for a := range accounts {
		names = append(names, a)
	}
	sort.Strings(names)
	return WTFMeta{Accounts: names, Files: len(entries), Bytes: bytes, Digest: treeDigest(entries)}
}

func buildManifest(install string, flavors, excludes []string, follow bool) (*Manifest, []string) {
	var warns []string
	host, _ := os.Hostname()
	m := &Manifest{
		ManifestVersion: manifestVersion,
		Tool:            "wowbak",
		CreatedUTC:      time.Now().UTC().Format(time.RFC3339),
		Source: Source{
			OS:          osLabel(),
			Platform:    platformLabel(),
			Hostname:    host,
			InstallPath: install,
		},
		Excludes:       excludes,
		FollowSymlinks: follow,
		Flavors:        map[string]*FlavorData{},
	}
	for _, f := range flavors {
		m.Flavors[f] = scanFlavor(install, f, excludes, follow, &warns)
	}
	return m, warns
}

func printManifest(m *Manifest, detail bool) {
	fmt.Printf("install   %s\n", m.Source.InstallPath)
	fmt.Printf("source    %s (%s)\n", m.Source.OS, m.Source.Hostname)
	fmt.Printf("created   %s\n", m.CreatedUTC)
	if m.Note != "" {
		fmt.Printf("note      %s\n", m.Note)
	}
	for _, flavor := range sortedFlavors(m.Flavors) {
		d := m.Flavors[flavor]
		fmt.Printf("\n%s\n", flavor)
		fmt.Printf("  addons  %d addons, %d files\n", len(d.Addons), d.Totals.Files-d.WTF.Files)
		acct := ""
		if len(d.WTF.Accounts) > 0 {
			acct = ", accounts: " + strings.Join(d.WTF.Accounts, ", ")
		}
		fmt.Printf("  wtf     %d files, %s%s\n", d.WTF.Files, human(d.WTF.Bytes), acct)
		fmt.Printf("  total   %d files, %s\n", d.Totals.Files, human(d.Totals.Bytes))
		if detail {
			names := make([]string, 0, len(d.Addons))
			for n := range d.Addons {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				a := d.Addons[n]
				v := a.Version
				if v == "" {
					v = "-"
				}
				fmt.Printf("    %-32s %-14s %4d files  %s\n", n, v, a.Files, human(a.Bytes))
			}
		}
	}
}

func printWarnings(warns []string) {
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
}
