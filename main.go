package main

import (
	"flag"
	"fmt"
	"os"
)

type stringList []string

func (s *stringList) String() string     { return fmt.Sprint(*s) }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

type scanArgs struct {
	installPath    string
	flavor         stringList
	addons         bool
	followSymlinks bool
}

type backupArgs struct {
	installPath    string
	flavor         stringList
	output         string
	force          bool
	includeConfig  bool
	followSymlinks bool
}

type inspectArgs struct {
	archive string
	addons  bool
}

type diffArgs struct {
	archive     string
	against     string
	installPath string
	flavor      stringList
	files       bool
	all         bool
}

type restoreArgs struct {
	archive       string
	installPath   string
	flavor        stringList
	dryRun        bool
	force         bool
	noSafety      bool
	clean         bool
	replaceAddons bool
	mirror        bool
	createMissing bool
	addonsOnly    bool
	wtfOnly       bool
}

type updateArgs struct {
	installPath string
	flavor      stringList
	names       []string
	all         bool
	dryRun      bool
}

type sourcesArgs struct {
	installPath string
	flavor      stringList
	discover    bool
	all         bool
}

type addonsArgs struct {
	installPath string
	flavor      stringList
	untracked   bool
}

type guiArgs struct {
	port      int
	noBrowser bool
}

type selfUpdateArgs struct {
	check bool
	all   bool
}

type pruneArgs struct {
	machine string
	keep    int
	backups bool
	force   bool
}

type listArgs struct {
	dir string
}

type configArgs struct {
	action      string
	key         string
	value       string
	installPath string
}

const usage = `wowbak - back up WoW addons + WTF and move them between machines.

usage: wowbak <command> [flags]

commands:
  gui         open the point-and-click interface in your browser
  config      show or set the WoW install path ("config init" writes a template)
  addons      list installed addons, grouped into packages
  sources     show or discover where each addon's updates come from
  outdated    check installed addons against their latest release
  update      download and install addon updates
  token       save the optional GitHub token used for addon update checks
  version     print the version of wowbak you are running
  self-update update wowbak itself to the latest release
  list        list archives in the backup folder, grouped by machine
  machines    list the machines this config knows about
  prune       remove undo points (and optionally backups) for a machine
  scan        show what would be backed up
  backup      write a portable archive
  inspect     print an archive's metadata
  diff        compare archive metadata to this machine or another archive
  restore     unpack an archive into this machine's install

Run "wowbak <command> -h" for that command's flags.
`

func main() {
	defer func() {
		if r := recover(); r != nil {
			if fe, ok := r.(fatalError); ok {
				fmt.Fprintf(os.Stderr, "error: %s\n", fe.msg)
				os.Exit(2)
			}
			panic(r)
		}
	}()
	run()
}

func run() {
	// Clear leftovers from a previous self-update, now that the replaced binary
	// is no longer running.
	cleanStaleBinaries()

	// No arguments means someone double-clicked it: open the interface.
	if len(os.Args) < 2 {
		os.Exit(cmdGUI(guiArgs{}))
	}
	rest := os.Args[2:]

	switch os.Args[1] {
	case "config":
		var a configArgs
		fs := flag.NewFlagSet("config", flag.ExitOnError)
		fs.StringVar(&a.installPath, "install-path", "", "WoW folder to inspect")
		initForce := fs.Bool("force", false, "overwrite an existing config file")
		pos := parseArgs(fs, rest)
		if len(pos) > 0 && pos[0] == "init" {
			os.Exit(cmdConfigInit(*initForce))
		}
		if len(pos) > 0 {
			a.action = pos[0]
			if a.action != "set" && a.action != "unset" {
				fatalf("config takes no action, or 'init' / 'set' / 'unset'")
			}
			a.key, a.value = optionalArg(pos, 1), optionalArg(pos, 2)
			if a.key == "" {
				fatalf("config %s needs a key, e.g. install_path", a.action)
			}
			if a.action == "set" && a.value == "" {
				fatalf("config set %s needs a value", a.key)
			}
		}
		os.Exit(cmdConfig(a))

	case "scan":
		var a scanArgs
		fs := flag.NewFlagSet("scan", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.BoolVar(&a.addons, "addons", false, "list every addon")
		followFlag(fs, &a.followSymlinks)
		rejectExtra(parseArgs(fs, rest), "scan")
		os.Exit(cmdScan(a))

	case "backup":
		var a backupArgs
		fs := flag.NewFlagSet("backup", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.StringVar(&a.output, "o", "", "archive path (default: wowbak-<timestamp>.zip)")
		fs.StringVar(&a.output, "output", "", "archive path (default: wowbak-<timestamp>.zip)")
		fs.BoolVar(&a.force, "force", false, "overwrite an existing archive")
		fs.BoolVar(&a.includeConfig, "include-config", false,
			"also archive WTF/Config.wtf (machine-specific graphics settings)")
		followFlag(fs, &a.followSymlinks)
		rejectExtra(parseArgs(fs, rest), "backup")
		os.Exit(cmdBackup(a))

	case "inspect":
		var a inspectArgs
		fs := flag.NewFlagSet("inspect", flag.ExitOnError)
		fs.BoolVar(&a.addons, "addons", false, "list every addon")
		a.archive = requireArg(parseArgs(fs, rest), 0, "inspect needs an archive path")
		os.Exit(cmdInspect(a))

	case "diff":
		var a diffArgs
		fs := flag.NewFlagSet("diff", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.BoolVar(&a.files, "files", false, "list every changed WTF file")
		fs.BoolVar(&a.all, "all", false, "include identical addons")
		pos := parseArgs(fs, rest)
		a.archive = requireArg(pos, 0, "diff needs an archive path")
		a.against = optionalArg(pos, 1)
		os.Exit(cmdDiff(a))

	case "restore":
		var a restoreArgs
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.BoolVar(&a.dryRun, "dry-run", false, "show the plan, write nothing")
		fs.BoolVar(&a.force, "force", false, "allow overwriting existing files")
		fs.BoolVar(&a.noSafety, "no-safety", false, "skip the pre-restore safety archive")
		fs.BoolVar(&a.clean, "clean", false, "set aside local addons that are not in the archive")
		fs.BoolVar(&a.replaceAddons, "replace-addons", false,
			"replace addon folders outright instead of merging, removing leftover files")
		fs.BoolVar(&a.mirror, "mirror", false,
			"make this machine match the backup exactly, instead of merging into it")
		fs.BoolVar(&a.createMissing, "create-missing", false, "create flavor dirs absent here")
		fs.BoolVar(&a.addonsOnly, "addons-only", false, "restore only Interface/AddOns")
		fs.BoolVar(&a.wtfOnly, "wtf-only", false, "restore only WTF")
		a.archive = requireArg(parseArgs(fs, rest), 0, "restore needs an archive path")
		os.Exit(cmdRestore(a))

	case "list":
		var a listArgs
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		fs.StringVar(&a.dir, "dir", "", "folder to list (default: the configured backup folder)")
		rejectExtra(parseArgs(fs, rest), "list")
		os.Exit(cmdList(a))

	case "gui":
		var a guiArgs
		fs := flag.NewFlagSet("gui", flag.ExitOnError)
		fs.IntVar(&a.port, "port", 0, "port to listen on (default: any free port)")
		fs.BoolVar(&a.noBrowser, "no-browser", false, "print the address instead of opening a browser")
		rejectExtra(parseArgs(fs, rest), "gui")
		os.Exit(cmdGUI(a))

	case "sources":
		var a sourcesArgs
		fs := flag.NewFlagSet("sources", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.BoolVar(&a.discover, "discover", false, "look up missing sources and save them")
		fs.BoolVar(&a.all, "all", false, "list every package, not just a summary")
		rejectExtra(parseArgs(fs, rest), "sources")
		os.Exit(cmdSources(a))

	case "outdated":
		var a updateArgs
		fs := flag.NewFlagSet("outdated", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.BoolVar(&a.all, "all", false, "also list up-to-date and unsourced addons")
		rejectExtra(parseArgs(fs, rest), "outdated")
		os.Exit(cmdOutdated(a))

	case "update":
		var a updateArgs
		fs := flag.NewFlagSet("update", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.BoolVar(&a.all, "all", false, "update everything that is outdated")
		fs.BoolVar(&a.dryRun, "dry-run", false, "show what would be updated, change nothing")
		a.names = parseArgs(fs, rest)
		os.Exit(cmdUpdate(a))

	case "token":
		fs := flag.NewFlagSet("token", flag.ExitOnError)
		pos := parseArgs(fs, rest)
		os.Exit(cmdToken(pos))

	case "addons":
		var a addonsArgs
		fs := flag.NewFlagSet("addons", flag.ExitOnError)
		installFlag(fs, &a.installPath, &a.flavor)
		fs.BoolVar(&a.untracked, "untracked", false, "only show packages with no provider id")
		rejectExtra(parseArgs(fs, rest), "addons")
		os.Exit(cmdAddons(a))

	case "version", "--version", "-v":
		os.Exit(cmdVersion())

	case "self-update":
		var a selfUpdateArgs
		fs := flag.NewFlagSet("self-update", flag.ExitOnError)
		fs.BoolVar(&a.check, "check", false, "report whether an update exists, install nothing")
		fs.BoolVar(&a.all, "all", false,
			"also update the other platforms' binaries in this folder")
		rejectExtra(parseArgs(fs, rest), "self-update")
		os.Exit(cmdSelfUpdate(a))

	case "prune":
		var a pruneArgs
		fs := flag.NewFlagSet("prune", flag.ExitOnError)
		fs.StringVar(&a.machine, "machine", "", "machine to prune (default: this one)")
		fs.IntVar(&a.keep, "keep", 0, "keep this many of the newest, remove the rest")
		fs.BoolVar(&a.backups, "backups", false,
			"also remove ordinary backups, not just undo points")
		fs.BoolVar(&a.force, "force", false, "actually delete; without this it only lists")
		pos := parseArgs(fs, rest)
		if len(pos) > 0 && a.machine == "" {
			a.machine = pos[0]
		}
		os.Exit(cmdPrune(a))

	case "machines":
		fs := flag.NewFlagSet("machines", flag.ExitOnError)
		rejectExtra(parseArgs(fs, rest), "machines")
		os.Exit(cmdMachines())

	case "-h", "--help", "help":
		fmt.Print(usage)
		os.Exit(0)

	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func rejectExtra(pos []string, cmd string) {
	if len(pos) > 0 {
		fatalf("%s takes no positional arguments, got %q", cmd, pos[0])
	}
}

func installFlag(fs *flag.FlagSet, install *string, flavor *stringList) {
	fs.StringVar(install, "install-path", "", "WoW folder (overrides config and autodetection)")
	fs.Var(flavor, "flavor", "game version to act on, repeatable (default: all)")
}

func followFlag(fs *flag.FlagSet, follow *bool) {
	fs.BoolVar(follow, "follow-symlinks", false, "archive symlinked addons by content")
}

// parseArgs parses flags that appear anywhere, before or after positional
// arguments. The stdlib flag package stops at the first non-flag token, which
// would silently ignore "diff backup.zip --install-path X" and quietly fall back
// to autodetection - dangerous when the fallback is a real install.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			return positional
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func requireArg(pos []string, i int, msg string) string {
	if len(pos) <= i {
		fatalf("%s", msg)
	}
	return pos[i]
}

func optionalArg(pos []string, i int) string {
	if len(pos) <= i {
		return ""
	}
	return pos[i]
}
