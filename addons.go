package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Provider names, as they appear in .toc metadata and in wowbak.conf.
const (
	providerCurse = "curse"
	providerWago  = "wago"
	providerWowI  = "wowi"
	providerTukui = "tukui"
)

// tocIDKeys maps a .toc metadata key to the provider it identifies.
var tocIDKeys = map[string]string{
	"x-curse-project-id": providerCurse,
	"x-wago-id":          providerWago,
	"x-wowi-id":          providerWowI,
	"x-tukui-projectid":  providerTukui,
}

// known pairs an already-identified folder with the package that owns it.
type known struct {
	folder string
	pkg    *Package
}

// addonFolder is one directory under Interface/AddOns.
type addonFolder struct {
	Name       string
	Title      string
	Version    string
	Interface  int
	Provider   string
	ProviderID string
	Website    string
}

// Package is what a user thinks of as "an addon": one download that may unpack
// into many folders. DBM is 27 folders; Details is 14.
type Package struct {
	Name       string   // display name, taken from the primary folder
	Folders    []string // every folder this package owns, sorted
	Version    string
	Interface  int // .toc Interface number, used to pick the right build
	Provider   string
	ProviderID string
	Website    string
}

func (p Package) tracked() bool { return p.Provider != "" && p.ProviderID != "" }

func (p Package) ref() string {
	if !p.tracked() {
		return "-"
	}
	return p.Provider + ":" + p.ProviderID
}

func readAddonFolder(root, name string) addonFolder {
	af := addonFolder{Name: name}
	dir := filepath.Join(root, name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return af
	}
	var tocs []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".toc") {
			tocs = append(tocs, e.Name())
		}
	}
	sort.Strings(tocs)
	// Prefer <Folder>.toc; flavor variants like <Folder>_Mainline.toc are fallbacks.
	sort.SliceStable(tocs, func(i, j int) bool {
		return strings.EqualFold(tocs[i], name+".toc")
	})

	for _, tocName := range tocs {
		data, err := os.ReadFile(filepath.Join(dir, tocName))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "##") {
				continue
			}
			key, value, found := strings.Cut(line[2:], ":")
			if !found {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			switch {
			case key == "version" && af.Version == "":
				af.Version = value
			case key == "interface" && af.Interface == 0:
				// May be a comma-separated list; the first entry is enough.
				first, _, _ := strings.Cut(value, ",")
				if n, err := strconv.Atoi(strings.TrimSpace(first)); err == nil {
					af.Interface = n
				}
			case key == "title" && af.Title == "":
				af.Title = stripColorCodes(value)
			case key == "x-website" && af.Website == "":
				af.Website = value
			default:
				if prov, ok := tocIDKeys[key]; ok && af.Provider == "" {
					af.Provider, af.ProviderID = prov, value
				}
			}
		}
	}
	return af
}

// stripColorCodes removes WoW's |cffRRGGBB ... |r markup from a title.
func stripColorCodes(s string) string {
	for {
		i := strings.Index(s, "|c")
		if i < 0 || len(s) < i+10 {
			break
		}
		s = s[:i] + s[i+10:]
	}
	return strings.ReplaceAll(s, "|r", "")
}

// scanPackages groups the folders of one game version into packages. Folders are
// joined when they share a provider ID, or when an unidentified folder's name
// extends an identified one (WeakAurasOptions -> WeakAuras), or when both reduce
// to the same leading token (DBM-Party-BfA -> DBM).
func scanPackages(install, flavor string) []Package {
	root := filepath.Join(install, flavor, "Interface", "AddOns")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var folders []addonFolder
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".wowbak-removed") {
			continue
		}
		folders = append(folders, readAddonFolder(root, e.Name()))
	}

	// Seed one group per distinct provider ID.
	groups := map[string]*Package{}
	keyOf := func(af addonFolder) string { return af.Provider + ":" + af.ProviderID }
	for _, af := range folders {
		if af.Provider == "" {
			continue
		}
		k := keyOf(af)
		p := groups[k]
		if p == nil {
			p = &Package{Provider: af.Provider, ProviderID: af.ProviderID}
			groups[k] = p
		}
		p.Folders = append(p.Folders, af.Name)
		// The shortest folder name is the best display name and version source:
		// "DBM-Core" over "DBM-Party-WotLK".
		if p.Name == "" || len(af.Name) < len(p.Name) {
			p.Name, p.Version, p.Website = af.Name, af.Version, af.Website
			p.Interface = af.Interface
		}
	}

	// Attach unidentified folders to the group they most likely belong to.
	// Iteration order must not affect the result, so this walks a sorted list.
	var knowns []known
	for _, p := range groups {
		for _, f := range p.Folders {
			knowns = append(knowns, known{strings.ToLower(f), p})
		}
	}
	sort.Slice(knowns, func(i, j int) bool { return knowns[i].folder < knowns[j].folder })
	var loose []addonFolder
	for _, af := range folders {
		if af.Provider != "" {
			continue
		}
		if p := matchGroup(af.Name, knowns); p != nil {
			p.Folders = append(p.Folders, af.Name)
			continue
		}
		loose = append(loose, af)
	}

	// Whatever is left stands alone: self-hosted, hand-installed, or your own.
	var out []Package
	for _, p := range groups {
		sort.Strings(p.Folders)
		out = append(out, *p)
	}
	for _, af := range loose {
		out = append(out, Package{
			Name: af.Name, Folders: []string{af.Name},
			Version: af.Version, Interface: af.Interface, Website: af.Website,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// matchGroup decides which package an unidentified folder belongs to by scoring
// each identified folder on how many leading name tokens they share
// (DBM-Party-BfA shares [dbm party] with DBM-Party-BC, but only [dbm] with
// DBM-Core, so it joins the party mods).
//
// It returns nil unless one package wins outright. Guessing between equally
// plausible packages would put folders in the wrong group, and the updater
// replaces folders by group - so an ambiguous folder is left standing alone,
// where nothing will touch it.
func matchGroup(name string, knowns []known) *Package {
	mine := nameTokens(strings.ToLower(name))

	// Score each package by its best-matching folder. Several folders of the
	// same package matching is not ambiguity - it is the package matching well.
	type cand struct {
		pkg   *Package
		score int
	}
	var cands []cand
	index := map[*Package]int{}
	for _, k := range knowns { // sorted, so ties resolve the same way every run
		score := sharedTokenPrefix(mine, nameTokens(k.folder))
		if score == 0 {
			continue
		}
		if i, seen := index[k.pkg]; seen {
			if score > cands[i].score {
				cands[i].score = score
			}
			continue
		}
		index[k.pkg] = len(cands)
		cands = append(cands, cand{k.pkg, score})
	}
	if len(cands) == 0 {
		return nil
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.score > best.score {
			best = c
		}
	}
	for _, c := range cands {
		if c.pkg != best.pkg && c.score == best.score {
			return nil // two packages fit equally well; do not guess
		}
	}
	return best.pkg
}

// nameTokens splits an addon folder name on - and _ into lowercase parts.
func nameTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' })
}

func sharedTokenPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func cmdAddons(args addonsArgs) int {
	install := resolveInstall(args.installPath)
	flavors := resolveFlavors(install, args.flavor)

	for _, flavor := range flavors {
		pkgs := scanPackages(install, flavor)
		if len(pkgs) == 0 {
			continue
		}
		folders, tracked := 0, 0
		for _, p := range pkgs {
			folders += len(p.Folders)
			if p.tracked() {
				tracked++
			}
		}
		fmt.Printf("%s - %d packages in %d folders\n\n", flavor, len(pkgs), folders)
		for _, p := range pkgs {
			if args.untracked && p.tracked() {
				continue
			}
			extra := ""
			if n := len(p.Folders); n > 1 {
				extra = fmt.Sprintf("%d folders", n)
			} else {
				extra = "1 folder"
			}
			fmt.Printf("  %-30s %-12s %-11s %s\n", p.Name, dash(p.Version), extra, p.ref())
		}
		fmt.Printf("\n  %d tracked, %d untracked\n", tracked, len(pkgs)-tracked)
	}
	return 0
}
