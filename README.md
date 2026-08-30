# wowbak

Back up World of Warcraft addons + WTF settings and move them to another machine,
even when the two machines run different operating systems.

Written in Go and compiled to a **standalone binary with no runtime dependency**.
Nothing to install on the target machine, and **no admin rights needed to run it** —
copy the binary next to the archive and go.

## Build

```
cd wowbak && ./build.sh
```

Produces `dist/WowBackup/` — copy that whole folder to a USB stick:

```
WowBackup/
  WowBackup.exe           double-click launcher (Windows, no console)
  WowBackup.app           double-click launcher (macOS, universal)
  START-HERE.txt          plain-language instructions
  wowbak.conf             editable text config
  backups/                archives, one subfolder per machine
    gaming-pc/
    my-laptop/
  wowbak.exe              Windows x64
  wowbak-arm64.exe        Windows on ARM
  wowbak-macos            Apple Silicon
  wowbak-macos-intel      Intel Mac
  wowbak-linux            Linux x64
  wowbak-linux-arm64      Linux ARM
```

Binaries are ~3 MB each and statically linked. The two launchers open the graphical
interface; the plain `wowbak*` binaries are the command-line tool. Everything the tool needs lives in
that folder, so the stick is self-contained: config, backups, and a binary for
whatever machine you plug into.

### Windows notes

Running an unsigned binary may raise a SmartScreen prompt ("Windows protected your
PC") if it arrived via browser or email. Dismissing it is a normal user action —
**More info → Run anyway** — and does *not* require admin. Copying via USB or a
network share usually avoids the prompt entirely. A locked-down corporate machine
running AppLocker can hard-block unsigned binaries; that case does need admin.

The tool never needs elevation itself, but *writing to the WoW folder* might if the
game is installed under `C:\Program Files (x86)`. `wowbak config` reports writability
per game version up front, and `restore` refuses to start if it cannot write, rather
than failing halfway through:

```
  _retail_    137 addon dirs, WTF present, writable
```

## Interface

Double-click `WowBackup.exe` or `WowBackup.app`, or run `wowbak gui`. The binary
serves a small page and opens your browser:

```
wowbak interface running at:
  http://127.0.0.1:52413/?t=8a3e4338...
```

It shows the detected machine, install path and writability; lists every backup
grouped by machine; and does backup, compare and restore with live output. Restore
always previews first and asks before writing. "Quit WowBackup" stops the server.

The **Addon updates** section checks installed addons against their latest
release and shows them in a table - installed version, latest, status - with an
Update button per addon and an Update all. "Find missing sources" runs discovery.
Outdated and failed addons are shown by default; "show everything" reveals the
rest. Each update writes an undo point first, exactly as the command line does.

Chosen over a native toolkit deliberately: Fyne and Wails need CGO plus platform
SDKs, which breaks single-command cross-compilation and the no-install promise. A
served page keeps one static binary that builds for every OS from any one machine,
and every machine already has a browser.

It listens on loopback only, with a random port and a session token required on
every request; `Host` and `Origin` are checked so a page you have open in another
tab cannot drive it.

## Config

`wowbak.conf` is a plain text file you edit in any editor — no JSON, no escaping
backslashes in Windows paths. `#` starts a comment.

```ini
install_path.gaming-pc = C:\Program Files (x86)\World of Warcraft
install_path.my-laptop = D:\Games\World of Warcraft
install_path.andys-mac = /Applications/World of Warcraft

backup_dir = backups
# flavors = _retail_, _classic_era_
# follow_symlinks = false
# exclude = *.lua.bak
```

A key can carry a **machine name** suffix. That machine's own entry wins, and an
untagged key is the fallback for any machine without one. Since a computer has one
OS, its entry covers the OS difference too — there is nothing else to configure.

Machine names come from the computer's network name, lowercased with any domain
suffix dropped (`Andys-MacBook.local` → `andys-macbook`). Override with
`WOWBAK_MACHINE` if two machines somehow share a name.

**New machines register themselves.** The first backup on an unrecognized computer
records what it detected:

```
new machine "gaming-pc" registered in wowbak.conf
  install_path.gaming-pc = C:\Program Files (x86)\World of Warcraft
```

`wowbak machines` lists every computer the stick knows, its OS, its configured path,
and how many backups it has:

```
  gaming-pc (Windows)     3 backup(s)
                          C:\Program Files (x86)\World of Warcraft
* andys-mac (macOS)       1 backup(s)
                          /Applications/World of Warcraft
```

The install path is the folder that *contains* `_retail_` / `_classic_era_`, not
one of those folders. Leave it unset to autodetect the usual locations.

`wowbak config` prints which file was loaded, the resolved path, every game version
found, and whether each is writable. `wowbak config init` writes a fresh template.

Resolution order, highest first:

```
wowbak backup --install-path "..."      # per run
WOWBAK_INSTALL_PATH=...                 # per shell
wowbak.conf next to the binary          # install_path.<machine>, then install_path
the per-user config folder              # normal installs
autodetection
```

The config is found by the binary's location, not the working directory, so the
stick's settings apply no matter where you run it from.

## Moving a setup between machines

```
# old machine - writes into backups/<this machine>/ on the stick
wowbak backup
wowbak list

# new machine - restore from the folder of the machine it came FROM
wowbak diff backups/gaming-pc/wowbak-20260829-120000.zip
wowbak restore backups/gaming-pc/wowbak-20260829-120000.zip --dry-run
wowbak restore backups/gaming-pc/wowbak-20260829-120000.zip --force
```

`wowbak backup` with no `-o` writes a timestamped archive into
`<backup_dir>/<machine>/`, so several computers share a stick without their archives
colliding. `wowbak list` shows every archive grouped by machine, newest first, with a
one-line summary read from each manifest and `(this machine)` marking your own.

Archives store OS-neutral relative paths (`_retail_/WTF/...`), so restore re-anchors
them to whatever install path the target machine resolves to. macOS → Windows → Linux
all work in any direction.

## Restoring one machine's setup onto another

This is the point of the per-machine folders: back up on one machine, restore on
any other, in either direction and across operating systems. Restore reads the
archive's OS-neutral paths and re-anchors them to whatever install path the
target machine resolves to.

There are two ways to do it, and they mean different things:

```
wowbak restore backups/gaming-pc/wowbak-DATE.zip --force            # add to this machine
wowbak restore backups/gaming-pc/wowbak-DATE.zip --mirror --force   # make this machine match
```

**Add** (the default) merges: the backup is copied in alongside what is already
installed, and addons you have that the backup does not are left alone. Good for
carrying your setup to a machine you also use for something else.

**Mirror** makes this machine match the one the backup came from: addon folders
are replaced outright rather than merged, addons not in the backup are set aside
as `<name>.wowbak-removed`, and game versions missing here are created. Good for
setting up a new machine.

Mirror still leaves two things alone, deliberately:

- **`WTF/Config.wtf`** - resolution and hardware settings stay machine-local.
- **Other accounts' settings.** WTF folders for Battle.net accounts the backup
  does not contain are kept. Deleting another account's saved variables would be
  destructive with nothing to gain.

Both modes preview with `--dry-run`, and both write an undo point before
changing anything. In the interface, Restore asks which of the two you want and
previews that choice before applying.

## Comparing metadata

`diff` compares an archive against this machine, or against a second archive:

```
wowbak diff backup.zip                 # archive vs. this machine
wowbak diff old.zip new.zip            # archive vs. archive
wowbak diff backup.zip --files --all   # every WTF file, plus identical addons
```

Addons compare by `.toc` version *and* by a content digest, so a version bump and a
silent content change are distinguished. WTF compares per file by SHA-256; mtimes are
recorded but never used for comparison, since they do not survive a transfer.

```
  + only in the archive     (restore adds it)
  - only on this machine    (restore leaves it; --clean sets it aside)
  ~ differs
  = identical               (--all)
```

Exit code is 0 when identical, 1 when anything differs. `diff` reuses the exclude
rules recorded in the archive so both sides are compared on equal terms.

## What is excluded by default

`.git`, `.svn`, `.DS_Store`, `Thumbs.db`, `desktop.ini`, `*.lua.bak`,
`*.wowbak-removed`, `**/config-cache.wtf`, and `WTF/Config.wtf` — the last because it
holds resolution and hardware settings that should stay machine-local.
`backup --include-config` overrides it.

Symlinked addons (dev checkouts) are skipped with a warning; `--follow-symlinks`
archives them by content instead.

Exclude patterns: no `/` matches a basename, a leading `**/` or `*/` matches a
basename at any depth, anything else matches the whole flavor-relative path.

## Addon updates

`wowbak` can check installed addons against their latest release and install
updates. Sources are GitHub releases only - free, documented, and permitted for
automated downloads, unlike CurseForge (approval-gated, revocable) and Wago
(paid tier).

```
wowbak sources --discover     # find each addon's repo, saved to wowbak.conf
wowbak outdated               # what is behind
wowbak update --all --dry-run # what would change
wowbak update --all           # do it
```

Discovery reads each addon's public Wago page once, purely to learn which
GitHub repo it lives in, then records it:

```ini
addon.weakauras = github:WeakAuras/WeakAuras2
addon.dbm-core  = github:DeadlyBossMods/DeadlyBossMods
```

All version checks and downloads then go through GitHub's API. Set a token to
lift the rate limit from 60 to 5000 requests an hour - see **Config** above.

### Picking the right build

Addons ship separate builds per game version, and installing a Classic build
over Retail breaks the addon. Matching is done on the `Interface` number in the
release's `release.json`, not on version names or tag text:

```
WeakAuras 5.21.11  ->  bcc, cata, classic, mists, titan   (no retail build)
```

So on a Retail install, WeakAuras 5.21.1 is correctly reported as current even
though 5.21.11 exists. A release with no build for your game version is skipped,
and an addon with no usable release is reported rather than guessed at.

Version comparison handles the formats addons actually use - `v12.0.21`, `335`,
`1.99422`, `12.1.0.7`, `1.2.3-4-gabc123` - numerically rather than lexically, so
`12.1.0.10` is newer than `12.1.0.7`. Flavor suffixes are ignored, so
`Plater-v653-Retail` and tag `Plater-v653` compare equal. Genuine pre-release
markers still order before a release. When two versions cannot be compared, the
addon is left alone.

### Undoing an update

Each update snapshots the folders it replaces into this machine's backup folder
first, and prints the command that puts them back:

```
undo: wowbak restore my-laptop/wowbak-pre-update-MythicDungeonTools-...zip --force --replace-addons
```

`--replace-addons` matters: a plain restore merges, which would leave files from
the newer version beside the older `.toc` - a combination that never shipped.
Add `--clean` as well to also set aside folders the newer version introduced.

## Clearing out undo points

Every restore and update writes an undo point, and they add up - a snapshot of a
large addon can be tens of megabytes.

```
wowbak prune                      # list this machine's undo points
wowbak prune --force              # delete them
wowbak prune gaming-pc --force    # another machine's
wowbak prune --keep 3 --force     # keep the three newest
wowbak prune --backups --force    # also remove ordinary backups
```

Without `--force` it only lists, so you always see what would go first. Ordinary
backups are never touched unless you pass `--backups`. In the interface each
machine shows a "Delete N undo point(s)" button when it has any.

## Restore safety

Restore refuses to overwrite until you pass `--force`, and then snapshots the files
it is about to replace into `wowbak-pre-restore-<timestamp>.zip` next to the archive
first (`--no-safety` to skip). The snapshot holds *this* machine's files, so it is
filed under this machine's backup folder. It is itself a valid wowbak archive, so
undoing a restore is just:

```
wowbak restore backups/my-laptop/wowbak-pre-restore-20260829-120500.zip --force --no-safety
```

Restore also checks it can write to every target folder before copying anything, and
aborts with "No files were changed" if it cannot — so a permissions problem never
leaves a half-applied setup.

`--clean` renames local addons missing from the archive to `<name>.wowbak-removed`
rather than deleting them.

Other flags: `--addons-only`, `--wtf-only`, `--flavor _retail_` (repeatable),
`--create-missing` to create flavor dirs absent on the target.

## Versions and updating wowbak itself

```
wowbak version              # what you are running
wowbak self-update --check  # is there anything newer
wowbak self-update          # update this binary
wowbak self-update --all    # update every binary in this folder
```

`--all` matters on a USB stick: the folder carries a binary per platform, and
updating only the one you happen to be running would leave the rest behind.

Releases are built by GitHub Actions from a `v*` tag and carry the version,
commit and build date. A binary built locally reports itself as `dev` and will
never offer to update, so a working copy is not replaced by a release.

Updating replaces a binary that may be running. The new file is downloaded and
checked in full before anything moves; the old one is renamed rather than
deleted, which is also the only way to replace a running program on Windows, and
is cleared away the next time wowbak starts. Your settings, token and backups are
never touched.

## Releasing

```
git tag v0.1.0 && git push origin v0.1.0
```

The workflow tests, cross-compiles every target, and publishes the portable
folder plus individual binaries and `checksums.txt` to a GitHub release.

## License

MIT - see [LICENSE](LICENSE).
