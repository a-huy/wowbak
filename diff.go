package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// cmdDiff compares archive metadata against this machine, or against a second archive.
func cmdDiff(args diffArgs) int {
	left := readManifest(args.archive)
	leftLabel := filepath.Base(args.archive)

	var right *Manifest
	var rightLabel string
	if args.against != "" {
		right = readManifest(args.against)
		rightLabel = filepath.Base(args.against)
	} else {
		install := resolveInstall(args.installPath)
		var flavors []string
		if len(args.flavor) > 0 {
			flavors = resolveFlavors(install, args.flavor)
		} else {
			// A flavor in the archive but not installed here is a difference to report,
			// not an error, so scan the intersection and let the loop below flag the rest.
			present := resolveFlavors(install, nil)
			for _, f := range present {
				if _, ok := left.Flavors[f]; ok {
					flavors = append(flavors, f)
				}
			}
			if len(flavors) == 0 {
				flavors = present
			}
		}
		excludes := left.Excludes
		if len(excludes) == 0 {
			excludes = defaultExcludes
		}
		fmt.Printf("scanning %s ...\n\n", install)
		right, _ = buildManifest(install, flavors, excludes, left.FollowSymlinks)
		rightLabel = "this machine"
	}

	fmt.Printf("  %s  ->  %s\n", leftLabel, rightLabel)
	fmt.Printf("  + only in %s (restore would add)\n", leftLabel)
	fmt.Printf("  - only in %s (restore leaves it, --clean removes it)\n", rightLabel)
	fmt.Printf("  ~ differs\n")

	names := map[string]bool{}
	for f := range left.Flavors {
		names[f] = true
	}
	for f := range right.Flavors {
		names[f] = true
	}
	all := make([]string, 0, len(names))
	for f := range names {
		all = append(all, f)
	}
	sort.Slice(all, func(i, j int) bool { return flavorRank(all[i]) < flavorRank(all[j]) })

	changed := 0
	for _, flavor := range all {
		lf, lok := left.Flavors[flavor]
		rf, rok := right.Flavors[flavor]
		fmt.Printf("\n=== %s ===\n", flavor)
		if !lok {
			fmt.Printf("  - present only in %s\n", rightLabel)
			continue
		}
		if !rok {
			fmt.Printf("  + present only in %s (restore would create it)\n", leftLabel)
			changed += lf.Totals.Files
			continue
		}
		changed += diffAddons(lf, rf, args.all)
		changed += diffWTF(lf, rf, args.files)
	}

	fmt.Printf("\n%d difference(s).\n", changed)
	if changed > 0 {
		return 1
	}
	return 0
}

func diffAddons(lf, rf *FlavorData, showIdentical bool) int {
	names := map[string]bool{}
	for n := range lf.Addons {
		names[n] = true
	}
	for n := range rf.Addons {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	var lines []string
	changed := 0
	for _, name := range sorted {
		l, lok := lf.Addons[name]
		r, rok := rf.Addons[name]
		switch {
		case !rok:
			lines = append(lines, fmt.Sprintf("  + %-32s %-14s %d files, %s",
				name, dash(l.Version), l.Files, human(l.Bytes)))
			changed++
		case !lok:
			lines = append(lines, fmt.Sprintf("  - %-32s %-14s %d files, %s",
				name, dash(r.Version), r.Files, human(r.Bytes)))
			changed++
		case l.Digest != r.Digest:
			detail := fmt.Sprintf("%s (same version, content differs)", dash(l.Version))
			if l.Version != "" && r.Version != "" && l.Version != r.Version {
				detail = fmt.Sprintf("%s -> %s", r.Version, l.Version)
			}
			lines = append(lines, fmt.Sprintf("  ~ %-32s %s", name, detail))
			changed++
		case showIdentical:
			lines = append(lines, fmt.Sprintf("  = %-32s %s", name, dash(l.Version)))
		}
	}

	fmt.Printf("\nAddOns  (%d in archive / %d local)\n", len(lf.Addons), len(rf.Addons))
	if len(lines) == 0 {
		fmt.Println("  identical")
	} else {
		fmt.Println(strings.Join(lines, "\n"))
	}
	return changed
}

func diffWTF(lf, rf *FlavorData, showFiles bool) int {
	left := filterPrefix(lf.Files, "WTF/")
	right := filterPrefix(rf.Files, "WTF/")

	var added, removed, modified []string
	for rel := range left {
		if r, ok := right[rel]; !ok {
			added = append(added, rel)
		} else if r.SHA256 != left[rel].SHA256 {
			modified = append(modified, rel)
		}
	}
	for rel := range right {
		if _, ok := left[rel]; !ok {
			removed = append(removed, rel)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(modified)

	fmt.Printf("\nWTF  (%d files in archive / %d local)\n", len(left), len(right))
	onlyLeft := sub(lf.WTF.Accounts, rf.WTF.Accounts)
	onlyRight := sub(rf.WTF.Accounts, lf.WTF.Accounts)
	if len(onlyLeft) > 0 {
		fmt.Printf("  + accounts only in archive: %s\n", strings.Join(onlyLeft, ", "))
	}
	if len(onlyRight) > 0 {
		fmt.Printf("  - accounts only local:      %s\n", strings.Join(onlyRight, ", "))
	}

	if len(added)+len(removed)+len(modified) == 0 {
		fmt.Println("  identical")
		return 0
	}

	if showFiles {
		for _, rel := range added {
			fmt.Printf("  + %s  (%s)\n", rel, human(left[rel].Size))
		}
		for _, rel := range removed {
			fmt.Printf("  - %s  (%s)\n", rel, human(right[rel].Size))
		}
		for _, rel := range modified {
			fmt.Printf("  ~ %s  (%s -> %s)\n", rel, human(right[rel].Size), human(left[rel].Size))
		}
	} else {
		fmt.Printf("  + %d added   - %d local-only   ~ %d changed   (--files to list them)\n",
			len(added), len(removed), len(modified))
		for i, rel := range modified {
			if i == 8 {
				fmt.Printf("    ... and %d more\n", len(modified)-8)
				break
			}
			fmt.Printf("    ~ %s\n", rel)
		}
	}
	return len(added) + len(removed) + len(modified)
}

func filterPrefix(files map[string]FileMeta, prefix string) map[string]FileMeta {
	out := map[string]FileMeta{}
	for k, v := range files {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out
}

func sub(a, b []string) []string {
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
