package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const confName = "wowbak.conf"

// machineID identifies this computer by its network name, normalized so it is
// safe as both a config key and a folder name. "Andys-MacBook.local" -> "andys-macbook".
func machineID() string {
	name := os.Getenv("WOWBAK_MACHINE")
	if name == "" {
		name, _ = os.Hostname()
	}
	if name == "" {
		name = os.Getenv("COMPUTERNAME") // Windows, if Hostname somehow failed
	}
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i] // drop .local / .lan / domain suffix
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown-machine"
	}
	return out
}

// osKey is a short, stable name for the current OS, used in output only.
func osKey() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

// exeDir is the folder the tool should treat as its own: the one holding the
// binary, or - when launched from a macOS .app bundle, whose executable lives at
// Foo.app/Contents/MacOS/Foo - the folder containing the bundle. That keeps the
// config and backups on the stick rather than buried inside the bundle.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if filepath.Base(dir) == "MacOS" {
		contents := filepath.Dir(dir)
		if filepath.Base(contents) == "Contents" {
			bundle := filepath.Dir(contents)
			if strings.EqualFold(filepath.Ext(bundle), ".app") {
				return filepath.Dir(bundle)
			}
		}
	}
	return dir
}

func userConfPath() string {
	var base string
	switch runtime.GOOS {
	case "windows":
		if base = os.Getenv("APPDATA"); base == "" {
			base, _ = os.UserHomeDir()
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support")
	default:
		if base = os.Getenv("XDG_CONFIG_HOME"); base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, "wowbak", confName)
}

// portableConfPath is the config sitting next to the binary. Present on a USB
// stick, absent for a normal per-user install.
func portableConfPath() string {
	dir := exeDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, confName)
}

// configSearch lists candidate config locations, most specific first.
func configSearch() []string {
	var out []string
	if env := os.Getenv("WOWBAK_CONFIG"); env != "" {
		out = append(out, env)
	}
	if p := portableConfPath(); p != "" {
		out = append(out, p)
	}
	out = append(out, userConfPath())
	return out
}

type Config struct {
	InstallPath    string
	BackupDir      string
	Flavors        []string
	Excludes       []string
	FollowSymlinks bool

	GitHubToken  string            // only if set in the config file; prefer wowbak.token
	Addons       map[string]string // lowercased package name -> "github:owner/repo"
	Machines     map[string]string // machine name -> its configured install path
	MachineKnown bool              // this machine has its own install_path line

	Path     string // file it came from; "" if none was found
	Portable bool   // true when Path sits next to the binary
}

// parseConf reads the editable key = value format. Lines starting with # or ;
// are comments. A key may be suffixed with an OS name (install_path.windows),
// which wins over the bare key on that OS - one stick, several machines.
func parseConf(path string, data string) Config {
	cfg := Config{Path: path, Machines: map[string]string{}, Addons: map[string]string{}}
	me := machineID()
	var bare, byMachine string

	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = unquote(strings.TrimSpace(value))
		if value == "" {
			continue
		}

		// A key may carry a .<machine> suffix.
		base, suffix := key, ""
		if b, s, ok := strings.Cut(key, "."); ok {
			base, suffix = b, s
		}

		if base == "addon" && suffix != "" {
			cfg.Addons[suffix] = value
			continue
		}

		if base == "install_path" {
			switch suffix {
			case "":
				bare = value
			case me:
				byMachine = value
			default:
				cfg.Machines[suffix] = value // another machine on this stick
			}
			continue
		}

		// Other settings may also be scoped to one machine.
		if suffix != "" && suffix != me {
			continue
		}

		switch base {
		case "backup_dir":
			cfg.BackupDir = value
		case "flavors":
			cfg.Flavors = append(cfg.Flavors, splitList(value)...)
		case "exclude", "excludes":
			cfg.Excludes = append(cfg.Excludes, splitList(value)...)
		case "github_token":
			cfg.GitHubToken = value
		case "follow_symlinks":
			b, _ := strconv.ParseBool(value)
			cfg.FollowSymlinks = b
		}
	}

	// This machine's own entry wins; the untagged key is the fallback.
	for _, v := range []string{byMachine, bare} {
		if v != "" {
			cfg.InstallPath = v
			break
		}
	}
	cfg.MachineKnown = byMachine != ""
	if byMachine != "" {
		cfg.Machines[me] = byMachine
	}
	return cfg
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadConfig() Config {
	for _, path := range configSearch() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		cfg := parseConf(path, string(data))
		cfg.Portable = path == portableConfPath()
		return cfg
	}
	return Config{}
}

func (c Config) excludesOrDefault() []string {
	if len(c.Excludes) > 0 {
		return c.Excludes
	}
	return defaultExcludes
}

// backupDir is where "backup" writes when no -o is given: a per-machine folder
// inside the backup root, so several machines can share one stick without
// their archives getting mixed up.
func (c Config) backupDir() string {
	return filepath.Join(c.backupRoot(), machineID())
}

// backupRoot holds one subfolder per machine.
func (c Config) backupRoot() string {
	if c.BackupDir != "" {
		base := filepath.Dir(c.Path)
		if !filepath.IsAbs(c.BackupDir) && base != "" && base != "." {
			return filepath.Join(base, c.BackupDir)
		}
		return expandHome(c.BackupDir)
	}
	if dir := exeDir(); dir != "" && fileExists(filepath.Join(dir, confName)) {
		return filepath.Join(dir, "backups")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// setConfValue rewrites one key in place, preserving comments and layout.
// Falls back to appending when the key is not already present.
func setConfValue(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, raw := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, _, found := strings.Cut(line, "=")
		if found && strings.EqualFold(strings.TrimSpace(k), key) {
			lines[i] = key + " = " + value
			replaced = true
			break
		}
	}
	if !replaced {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, key+" = "+value, "")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

const confTemplate = `# wowbak configuration
#
# Edit this file in any text editor. Lines starting with # are comments.
# It sits next to the wowbak binary, so it travels with the folder - put the
# whole folder on a USB stick and your settings come along.

# Where World of Warcraft is installed. This is the folder that CONTAINS
# _retail_ / _classic_era_, not one of those folders itself.
#
# A key can be tagged with a MACHINE NAME, so one stick serves several
# computers. Machine names come from the computer's network name, lowercased.
# wowbak fills these in automatically the first time it runs on a new machine.

# install_path.gaming-pc = C:\Program Files (x86)\World of Warcraft
# install_path.my-laptop = D:\Games\World of Warcraft
# install_path.andys-mac = /Applications/World of Warcraft

# An untagged line is the fallback for any machine without its own entry:
# install_path =

# Leave every install_path line commented out to autodetect the usual locations.
# Run "wowbak machines" to see which computers this stick knows about.

# Where "wowbak backup" writes archives. Relative paths are resolved against
# this file's folder, so the default keeps backups on the stick. Each machine
# gets its own subfolder inside it, e.g. backups/gaming-pc/
backup_dir = backups

# A GitHub token raises the addon-update rate limit from 60 to 5000 requests an
# hour. It is optional, needs no scopes, and reads only public data.
#
# Prefer "wowbak token set <token>", which writes it to wowbak.token next to this
# file with owner-only permissions, instead of putting it in here. Anything in
# this file is easy to share or copy by accident.
# github_token =

# Only back up these game versions. Blank means all that are installed.
# flavors = _retail_, _classic_era_

# Include symlinked addons (dev checkouts) by copying their contents.
# follow_symlinks = false

# Files to leave out. Repeat the key to add more; setting any "exclude" line
# replaces the built-in list below entirely.
#
# A pattern with no / matches a file or folder name anywhere.
# A leading **/ matches a name at any depth.
# Anything else matches the full path inside a game version folder.
#
# Built-in default:
#   exclude = .git, .svn, .DS_Store, Thumbs.db, desktop.ini
#   exclude = *.lua.bak, *.wowbak-removed, **/config-cache.wtf
#   exclude = WTF/Config.wtf
#
# WTF/Config.wtf holds resolution and hardware settings, so it is excluded by
# default to keep them machine-local. Remove it from the list to carry it over.
`

func cmdConfigInit(force bool) int {
	path := portableConfPath()
	if path == "" {
		path = userConfPath()
	}
	if fileExists(path) && !force {
		fatalf("%s already exists (use --force to overwrite it)", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatalf("cannot create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(confTemplate), 0o644); err != nil {
		fatalf("cannot write %s: %v", path, err)
	}
	fmt.Printf("wrote %s\n", path)
	fmt.Println("Open it in any text editor to set your install path.")
	return 0
}

// cmdList shows archives grouped by the machine that produced them.
func cmdList(args listArgs) int {
	cfg := loadConfig()
	root := args.dir
	if root == "" {
		root = cfg.backupRoot()
	}
	me := machineID()

	fmt.Printf("%s\n", root)
	groups := collectArchives(root)
	if len(groups) == 0 {
		fmt.Println("\n  no archives yet - run: wowbak backup")
		return 0
	}

	names := make([]string, 0, len(groups))
	for k := range groups {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, machine := range names {
		label := machine
		if machine == me {
			label += "   (this machine)"
		}
		fmt.Printf("\n%s\n", label)
		for _, r := range groups[machine] {
			fmt.Printf("  %-38s %10s  %s\n", r.name, human(r.size), r.mod.Format("2006-01-02 15:04"))
			fmt.Printf("  %-38s %s\n", "", r.summary)
		}
	}
	return 0
}

type archiveRow struct {
	name    string
	path    string
	size    int64
	mod     time.Time
	summary string
	os      string // OS recorded in the archive's manifest
}

// collectArchives reads <root>/<machine>/*.zip, and any loose zips directly in
// root, which are filed under "(unsorted)".
func collectArchives(root string) map[string][]archiveRow {
	groups := map[string][]archiveRow{}
	add := func(machine, dir string, e os.DirEntry) {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".zip") {
			return
		}
		info, err := e.Info()
		if err != nil {
			return
		}
		full := filepath.Join(dir, e.Name())
		summary, osName := summarizeArchive(full)
		groups[machine] = append(groups[machine], archiveRow{
			name: e.Name(), path: full, size: info.Size(), mod: info.ModTime(),
			summary: summary, os: osName,
		})
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return groups
	}
	for _, e := range entries {
		if e.IsDir() {
			sub := filepath.Join(root, e.Name())
			subEntries, err := os.ReadDir(sub)
			if err != nil {
				continue
			}
			for _, se := range subEntries {
				add(e.Name(), sub, se)
			}
			continue
		}
		add("(unsorted)", root, e)
	}
	for _, rows := range groups {
		sort.Slice(rows, func(i, j int) bool { return rows[i].mod.After(rows[j].mod) })
	}
	return groups
}

// cmdMachines lists every machine this stick knows about.
func cmdMachines() int {
	cfg := loadConfig()
	me := machineID()

	known := map[string]bool{}
	for name := range cfg.Machines {
		known[name] = true
	}
	counts := map[string]int{}
	osOf := map[string]string{}
	for name, rows := range collectArchives(cfg.backupRoot()) {
		if name != "(unsorted)" {
			known[name] = true
		}
		counts[name] = len(rows)
		for _, r := range rows { // rows are newest first
			if r.os != "" {
				osOf[name] = r.os
				break
			}
		}
	}
	known[me] = true
	if osOf[me] == "" {
		osOf[me] = osLabel()
	}

	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Printf("machines known to %s\n\n", cfg.pathOrNone())
	for _, n := range names {
		marker := "  "
		if n == me {
			marker = "* "
		}
		path := cfg.Machines[n]
		if path == "" {
			path = "(no install path configured)"
		}
		label := n
		if osName := osOf[n]; osName != "" {
			label = fmt.Sprintf("%s (%s)", n, osName)
		}
		fmt.Printf("%s%-28s %d backup(s)\n", marker, label, counts[n])
		fmt.Printf("  %-28s %s\n", "", path)
	}
	fmt.Printf("\n* = this machine (%s)\n", me)
	return 0
}

func (c Config) pathOrNone() string {
	if c.Path == "" {
		return "(no config file)"
	}
	return c.Path
}

// registerMachine records this machine's install path the first time it is seen,
// so plugging the stick into a new computer sets it up rather than silently
// reusing another machine's path.
func registerMachine(cfg Config, install string) {
	if cfg.MachineKnown {
		return
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
	key := "install_path." + machineID()
	if err := setConfValue(target, key, install); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record this machine in %s: %v\n", target, err)
		return
	}
	fmt.Printf("new machine %q registered in %s\n  %s = %s\n\n",
		machineID(), filepath.Base(target), key, install)
}

// summarizeArchive reads just the manifest, so listing stays fast on big archives.
// It returns the display line and the OS the archive was made on.
func summarizeArchive(path string) (string, string) {
	m := readManifestQuiet(path)
	if m == nil {
		return "(not a wowbak archive)", ""
	}
	if m.Note != "" {
		return "snapshot - " + m.Note, m.Source.OS
	}
	var parts []string
	for _, f := range sortedFlavors(m.Flavors) {
		d := m.Flavors[f]
		parts = append(parts, fmt.Sprintf("%s: %d addons, %d WTF files", f, len(d.Addons), d.WTF.Files))
	}
	return fmt.Sprintf("from %s - %s", m.Source.OS, strings.Join(parts, "; ")), m.Source.OS
}

const tokenFileName = "wowbak.token"

// tokenPath is the dedicated secret file, kept beside the config but separate
// from it: the config is meant to be read, edited and shared, a token is not.
func tokenPath() string {
	if c := loadConfig(); c.Path != "" {
		return filepath.Join(filepath.Dir(c.Path), tokenFileName)
	}
	if d := exeDir(); d != "" {
		return filepath.Join(d, tokenFileName)
	}
	return filepath.Join(filepath.Dir(userConfPath()), tokenFileName)
}

// githubToken finds a GitHub token and reports where it came from, so the user
// can tell which one is in play. Order: environment, then the token file, then
// the config file. It never returns the token in the source string.
func (c Config) githubToken() (token, source string) {
	for _, name := range []string{"WOWBAK_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, "environment (" + name + ")"
		}
	}
	if p := tokenPath(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			if v := strings.TrimSpace(string(data)); v != "" {
				return v, tokenFileName
			}
		}
	}
	if v := strings.TrimSpace(c.GitHubToken); v != "" {
		return v, filepath.Base(c.Path) + " (github_token)"
	}
	return "", ""
}

// maskToken shows just enough to tell two tokens apart.
func maskToken(t string) string {
	if len(t) <= 8 {
		return strings.Repeat("*", len(t))
	}
	return t[:4] + strings.Repeat("*", 6) + t[len(t)-4:]
}

// saveGitHubToken writes the token to its own file, readable only by this user.
func saveGitHubToken(tok string) (string, error) {
	path := tokenPath()
	if path == "" {
		return "", fmt.Errorf("nowhere to save a token")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// 0600 so it is not readable by other accounts on a shared machine.
	if err := os.WriteFile(path, []byte(strings.TrimSpace(tok)+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func cmdToken(pos []string) int {
	cfg := loadConfig()

	if len(pos) == 0 || pos[0] == "show" {
		tok, src := cfg.githubToken()
		if tok == "" {
			fmt.Println("No GitHub token set.")
			fmt.Println("\nAddon update checks work without one, but GitHub allows only")
			fmt.Println("60 requests an hour; a token raises that to 5000.")
			fmt.Println("\nCreate one at https://github.com/settings/tokens")
			fmt.Println("  - a fine-grained token with NO repository access is enough")
			fmt.Println("  - it only ever reads public release information")
			fmt.Printf("\nThen save it with:\n  wowbak token set <token>\n")
			fmt.Printf("\nIt will be written to:\n  %s\n", tokenPath())
			return 0
		}
		fmt.Printf("GitHub token  %s\n", maskToken(tok))
		fmt.Printf("from          %s\n", src)
		return 0
	}

	switch pos[0] {
	case "set":
		if len(pos) < 2 || strings.TrimSpace(pos[1]) == "" {
			fatalf("usage: wowbak token set <token>")
		}
		path, err := saveGitHubToken(pos[1])
		if err != nil {
			fatalf("could not save the token: %v", err)
		}
		fmt.Printf("saved to %s (readable only by you)\n", path)
		if d := exeDir(); d != "" && strings.HasPrefix(path, d) {
			fmt.Println("\nNote: this file sits alongside the tool, so it travels with the")
			fmt.Println("folder. If that folder is a USB stick you carry around, prefer")
			fmt.Println("setting WOWBAK_GITHUB_TOKEN in your environment instead, and")
			fmt.Printf("delete %s.\n", tokenFileName)
		}
		return 0
	case "clear":
		p := tokenPath()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fatalf("could not remove %s: %v", p, err)
		}
		fmt.Printf("removed %s\n", p)
		return 0
	}
	fatalf("unknown token command %q; use show, set or clear", pos[0])
	return 2
}

// isSnapshot reports whether an archive is an undo point rather than a backup
// you deliberately took.
func isSnapshot(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "pre-restore") || strings.Contains(n, "pre-update")
}

// cmdPrune removes archives from a machine's backup folder. It lists what it
// would remove and does nothing unless --force is given, because these are the
// only copies of whatever they hold.
func cmdPrune(args pruneArgs) int {
	cfg := loadConfig()
	root := cfg.backupRoot()
	machine := args.machine
	if machine == "" {
		machine = machineID()
	}

	groups := collectArchives(root)
	rows, ok := groups[machine]
	if !ok || len(rows) == 0 {
		fmt.Printf("No archives for %q in %s\n", machine, root)
		if len(groups) > 0 {
			names := make([]string, 0, len(groups))
			for k := range groups {
				names = append(names, k)
			}
			sort.Strings(names)
			fmt.Printf("Machines with archives: %s\n", strings.Join(names, ", "))
		}
		return 0
	}

	// rows are newest first
	var candidates []archiveRow
	for _, r := range rows {
		if args.backups {
			candidates = append(candidates, r)
			continue
		}
		if isSnapshot(r.name) {
			candidates = append(candidates, r)
		}
	}
	if args.keep > 0 && len(candidates) > args.keep {
		candidates = candidates[args.keep:] // keep the newest N
	} else if args.keep > 0 {
		candidates = nil
	}

	kind := "snapshot"
	if args.backups {
		kind = "archive"
	}
	if len(candidates) == 0 {
		fmt.Printf("Nothing to remove: %s has no %ss matching.\n", machine, kind)
		return 0
	}

	var total int64
	fmt.Printf("%s - %d %s(s) in %s\n\n", machine, len(candidates), kind, filepath.Join(root, machine))
	for _, r := range candidates {
		total += r.size
		fmt.Printf("  %-42s %10s  %s\n", r.name, human(r.size), r.mod.Format("2006-01-02 15:04"))
	}
	fmt.Printf("\n%s in total\n", human(total))

	if !args.force {
		fmt.Println("\nNothing was deleted. Re-run with --force to remove these.")
		return 0
	}
	removed := 0
	for _, r := range candidates {
		if err := os.Remove(r.path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", r.name, err)
			continue
		}
		removed++
	}
	fmt.Printf("\nremoved %d file(s), freeing %s\n", removed, human(total))
	return 0
}
