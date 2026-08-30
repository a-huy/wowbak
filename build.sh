#!/bin/sh
# Build the portable WowBackup folder: binaries for every OS, an editable config,
# and a backups/ folder. Copy dist/WowBackup to a USB stick and it runs anywhere,
# no install and no admin rights.
set -e
cd "$(dirname "$0")"

OUT=dist/WowBackup

# Never delete this folder. It is a working install: it holds your settings
# (wowbak.conf), your GitHub token (wowbak.token) and your backups. Only the
# files this script produces are overwritten.
mkdir -p "$OUT/backups"
rm -rf "$OUT/WowBackup.app"   # a directory, so it is replaced rather than merged

build() {
  GOOS=$1 GOARCH=$2 go build -trimpath -ldflags="-s -w" -o "$OUT/$3" .
  echo "  $3"
}

echo "building command-line tools:"
build windows amd64 wowbak.exe
build windows arm64 wowbak-arm64.exe
build darwin  arm64 wowbak-macos
build darwin  amd64 wowbak-macos-intel
build linux   amd64 wowbak-linux
build linux   arm64 wowbak-linux-arm64

# Double-clickable launchers that go straight to the interface.
echo "building double-click launchers:"

# -H windowsgui drops the console window, so Windows shows only the browser.
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -H windowsgui" \
  -o "$OUT/WowBackup.exe" .
echo "  WowBackup.exe"

# A .app bundle so Finder treats it as an application. Universal, so it runs on
# both Apple Silicon and Intel.
APP="$OUT/WowBackup.app/Contents"
mkdir -p "$APP/MacOS"
if command -v lipo >/dev/null 2>&1; then
  lipo -create "$OUT/wowbak-macos" "$OUT/wowbak-macos-intel" -output "$APP/MacOS/WowBackup"
else
  cp "$OUT/wowbak-macos" "$APP/MacOS/WowBackup"
fi
chmod +x "$APP/MacOS/WowBackup"
cat > "$APP/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>WowBackup</string>
  <key>CFBundleDisplayName</key><string>WowBackup</string>
  <key>CFBundleIdentifier</key><string>local.wowbak.app</string>
  <key>CFBundleExecutable</key><string>WowBackup</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>LSUIElement</key><true/>
</dict>
</plist>
PLIST
echo "  WowBackup.app"

# The template is embedded in the binary; ask it for a copy so there is one source.
if [ ! -f "$OUT/wowbak.conf" ]; then
  "$OUT/$(go env GOOS | sed 's/darwin/wowbak-macos/;s/linux/wowbak-linux/;s/windows/wowbak.exe/')" \
    config init >/dev/null 2>&1 || true
  [ -f "$OUT/wowbak.conf" ] || echo "warning: could not generate wowbak.conf"
fi

cat > "$OUT/backups/README.txt" <<'EOF'
Backup archives land here.

Each .zip holds your addons and WTF settings plus a manifest describing where
they came from. Run "wowbak list" to see what is in this folder.
EOF

cp ../README.md "$OUT/README.md" 2>/dev/null || true

cat > "$OUT/START-HERE.txt" <<'EOF'
WowBackup - move your WoW addons and settings between machines
==============================================================

Copy this whole folder to a USB stick. Nothing needs to be installed on any
machine, and you do not need administrator rights to run it.

JUST DOUBLE-CLICK ONE OF THESE
------------------------------

  Windows    WowBackup.exe
  Mac        WowBackup.app

It opens WowBackup in your web browser. Everything below can be done by clicking:
see your backups, make a new one, compare one against this machine, and restore.

It is not a website - nothing leaves your computer. The page is served by the
program itself and is only reachable from this machine.

When you are finished, click "Quit WowBackup" in the page. It keeps running in
the background until you do.

The first time on a Mac you may need to right-click WowBackup.app and choose
Open, rather than double-clicking, because it is not signed by Apple.
On Windows you may see "Windows protected your PC" - click More info, then
Run anyway. Neither needs an administrator.


PREFER THE COMMAND LINE?
------------------------

  Windows              wowbak.exe
  Windows on ARM       wowbak-arm64.exe
  Mac (Apple Silicon)  wowbak-macos
  Mac (Intel)          wowbak-macos-intel
  Linux                wowbak-linux

Open a terminal in this folder and run them. On Windows, shift+right-click the
folder and choose "Open PowerShell window here", then type  .\wowbak.exe  followed
by a command. Run  wowbak.exe help  to see everything it can do.

If WowBackup.exe does not seem to do anything, run wowbak.exe gui from a terminal
instead - it prints the reason.


FIRST: check what it found
--------------------------
  wowbak config

wowbak identifies each computer by its network name and looks for WoW in the
usual places. The first time you back up on a new machine it records what it
found, so plugging the stick into another computer just works:

  new machine "gaming-pc" registered in wowbak.conf
    install_path.gaming-pc = C:\Program Files (x86)\World of Warcraft

If it guessed wrong, or found nothing, open wowbak.conf in any text editor
(Notepad is fine) and set the path yourself, tagged with that computer's name:

  install_path.gaming-pc = D:\Games\World of Warcraft

It is the folder that CONTAINS _retail_, not _retail_ itself.

  wowbak machines      which computers this stick knows about


ON THE OLD MACHINE: make a backup
---------------------------------
  wowbak backup

The archive goes into that machine's own folder on this stick, for example
backups/gaming-pc/ - so several computers can share one stick without their
backups getting mixed up.

  wowbak list          see every archive, grouped by machine


ON THE NEW MACHINE: put it back
-------------------------------
  wowbak list                                       find the archive you want
  wowbak diff backups/gaming-pc/wowbak-DATE.zip     what would change
  wowbak restore backups/gaming-pc/wowbak-DATE.zip --dry-run
  wowbak restore backups/gaming-pc/wowbak-DATE.zip --force

Note you restore from the folder of the machine the backup CAME FROM. That is
the whole point: take the gaming PC's setup and put it on the laptop.

diff and --dry-run change nothing, so look before you leap. restore will not
overwrite anything until you pass --force, and even then it first saves whatever
it replaces into another zip next to the archive.


NOTES
-----
Your graphics settings (WTF/Config.wtf) are deliberately NOT copied, so each
machine keeps its own resolution and quality settings.

If Windows shows "Windows protected your PC", click More info, then Run anyway.
That is Windows being cautious about a program it has not seen before; it does
not need an administrator.

Run any command with -h to see its options, e.g.  wowbak restore -h
EOF

echo
echo "portable folder ready: $OUT"
for f in wowbak.conf wowbak.token; do
  [ -f "$OUT/$f" ] && echo "  (kept your $f)"
done
ls -1 "$OUT"
