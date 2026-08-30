# Working in this repository

`wowbak` backs up World of Warcraft addons and settings, moves them between
machines running different operating systems, and updates addons from GitHub
releases. It is one Go binary with an embedded web interface, meant to run from a
USB stick with no installation and no administrator rights.

Read this before changing anything. Several rules below exist because breaking
them produced real, shipped bugs.

## Commands

```
go test ./...        # version comparison has real coverage; keep it passing
go vet ./...
gofmt -l .           # must print nothing; CI fails on any output
./build.sh           # builds dist/WowBackup for every platform
```

CI runs all four plus a cross-compile of every target on each push.

**Never pipe `./build.sh` through `head`, `tail` or anything that closes the pipe
early.** SIGPIPE kills the build part-way and leaves a stale binary in `dist/`,
which then looks like a broken code change. This has wasted debugging time twice.
Redirect to a file instead: `./build.sh > /tmp/build.log 2>&1`.

## Commit messages decide the version

Releases are automatic. release-please reads commit messages, works out the next
version, writes the changelog, and keeps a `chore(main): release x.y.z` pull
request open. Merging that pull request tags the release and builds the binaries.

| Prefix | Result |
|---|---|
| `fix:` | patch release |
| `feat:` | minor release |
| `feat!:` or `BREAKING CHANGE:` in the body | major release |
| `docs:` `chore:` `refactor:` `test:` | no release |

A commit with no recognised prefix produces **no release and no changelog entry**.
A bug fix written without `fix:` ships to nobody. Use the prefix that matches what
the change actually does, not the one that sounds impressive.

## `dist/WowBackup/` belongs to the user

It is a working install, not build output. It holds their settings
(`wowbak.conf`), their GitHub token (`wowbak.token`, mode 0600) and their backups.

- **Never delete it, and never delete files inside it.** `build.sh` deliberately
  does not remove the folder; it overwrites only the files it produces.
- Never commit any of it. `.gitignore` covers `dist/`, `*.token` and
  `wowbak.conf`, and it must stay that way.
- When testing, work in a temporary directory. Do not use the user's folder as
  scratch space.

## Layout

| File | Holds |
|---|---|
| `main.go` | command line parsing and dispatch |
| `manifest.go` | archive manifest types, `fatalf`, install-path resolution |
| `portable.go` | text config, machine identity, backup folders, token, prune |
| `scan.go` | walking an install, hashing, `.toc` parsing |
| `archive.go` | writing and reading backup archives |
| `diff.go` | comparing an archive against a machine or another archive |
| `restore.go` | restoring, mirroring, undo points |
| `addons.go` | grouping addon folders into packages |
| `version.go` | version-string comparison (tested) |
| `version_info.go` | build stamping, `wowbak version` |
| `github.go` | GitHub API client, release-asset selection |
| `sources.go` | mapping an addon to its GitHub repository |
| `update.go` | checking and applying addon updates |
| `selfupdate.go` | replacing wowbak's own binaries |
| `web.go` | local HTTP server behind the interface |
| `ui.html` | the interface, embedded with `go:embed` |

## Rules that are easy to break

**`fatalf` panics; it does not exit.** It is recovered in three places: the CLI
turns it into exit code 2, the job runner fails one job, and the HTTP guard
returns a 400. Never call `os.Exit` in code reachable from the server, or a bad
request takes the whole interface down.

**Jobs run one at a time.** Capturing output swaps the process-wide `os.Stdout`,
so overlapping jobs interleave their logs and clobber each other's restore. The
`runOne` mutex enforces this. Do not remove it.

**JSON sent to the browser must never contain `null` where the page expects a
list.** A nil `[]string` marshals to `null`, and `j.lines.map(...)` then throws,
freezing the page with no error. Initialise slices.

**Match addon builds on the `Interface` number**, never on tag text or flavor
names. WeakAuras 5.21.11 exists but ships only Classic builds; installing it over
a Retail install breaks the addon. `release.json` carries the interface number per
file; use it.

**Addon folders sharing a source are one package.** Multi-folder addons version
their sub-folders independently: Narcissus 1.8.6 installs Narcissus_BagFilter
1.0.2, whose version never catches up. Checked separately it reports outdated
forever and re-downloads 87MB every run. `groupBySource` merges them; the version
that counts comes from the folder the package is named after.

**Do not cap or time-limit downloads.** Release archives run to 90MB. A
`LimitReader` truncated one and produced "not a valid zip file"; a 30-second
client timeout then killed it at 80%. The JSON helper caps at 32MB, which is fine
for metadata; asset downloads go through `downloadTo`, which streams to disk with
no overall deadline.

**Version comparison ignores flavor words.** `Plater-v653-Retail` and tag
`Plater-v653` are the same version. Treating `-Retail` as a pre-release marker
reported it permanently outdated. `alpha`, `beta` and `rc` are deliberately *not*
in that list, since they really do order before a release.

**A local build reports `dev` and never self-updates.** That is what stops a
release from overwriting someone's working copy. `versionString` rejects Go's
pseudo-versions for the same reason.

**Restore merges; mirror replaces.** Merging is right for WTF, where deleting
another Battle.net account's saved variables would destroy settings for nothing.
`--replace-addons` exists because merging an addon folder leaves files from a
newer version beside an older `.toc` - a combination that never shipped.

## The web interface

Loopback only, random port, session token on every request, `Host` and `Origin`
checked. It can read and write the user's game folder, so a page open in another
tab must never be able to drive it. Keep those checks.

The token is never sent to the browser - only a masked form. Keep it that way.

Any action that changes the backup folder must call `refresh()` when it finishes,
or the list silently goes stale.

## macOS privacy prompts

macOS records file-access grants against an app's code signature. Go's default
ad-hoc signature identifies every binary as `a.out` and its hash changes on every
build, so a grant given once does not survive an update - macOS asks again.

`build.sh` re-signs the macOS builds with stable identifiers
(`local.wowbak.app`, `local.wowbak.cli`) so the entry in System Settings is at
least identifiable. It is still ad-hoc: only a Developer ID signature makes
grants persist across updates, and that needs a paid Apple account.

Do not try to work around the prompt in code. There is no API for it, and asking
is the correct behaviour for a tool that reads and writes a user's drives.

## Before you finish

- `gofmt -l .` prints nothing, `go vet` and `go test` pass
- No user file in `dist/WowBackup/` was moved, deleted or committed
- The commit message carries the right prefix for the change
