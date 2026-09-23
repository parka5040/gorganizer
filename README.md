# Gorganizer

Native Linux mod organizer for Bethesda games. A Go gRPC daemon
(`gorganizerd`) backs a Qt6 GUI; on activation, it merges your enabled mods
into the game's `Data/` folder via a hardlink farm. Built as a Linux-native
alternative to Mod Organizer 2 — no Wine, no Protontricks for the manager
itself.

**Status:** early. Targets Bethesda games: Skyrim SE, Skyrim, Fallout New
Vegas, Fallout 3, Fallout 4, Starfield, Oblivion, Oblivion Remastered,
Morrowind, and Tale of Two Wastelands (TTW).

Oblivion Remastered uses its nested `OblivionRemastered/Content/Dev/ObvData/Data`
directory, with OBSE64 and PAK/Win64/root-file mods handled alongside classic
ESP/ESM mods. Put game-root files under a mod's `.gorganizer-root/` directory;
Gorganizer deploys them as recoverable profile-scoped symlinks.

The External Tools dialog can install/update the official Windows portable
LOOT release, open it through the selected game's Proton prefix, or run an
isolated automatic sort. LOOT works from a disposable profile projection so it
cannot change hardlinked source mods. TTW may open LOOT in Fallout New Vegas
mode, but automatic sorting is intentionally disabled. Common Skyrim tools such
as xEdit, Creation Kit, Pandora, Nemesis, FNIS, BodySlide, DynDOLOD/xLODGen,
Synthesis, Wrye Bash, BethINI, and EasyNPC have built-in discovery and write
policies; their proprietary downloads are not redistributed.

## Install

```bash
git clone https://github.com/parka5040/gorganizer ~/gorganizer
cd ~/gorganizer
./gorganizer.sh
```

The clone path is up to you — `~/Apps/gorganizer` works just as well. On
first run the script:

1. Detects your distro (Arch, Debian/Ubuntu, Fedora, openSUSE) and prompts
   `[Y/n]` to install build dependencies via the system package manager.
2. Builds the Go daemon and Qt6 GUI in-tree.
3. Migrates any `*_Mods/` folders left behind by a previous install.
4. Installs a `gorganizer.desktop` entry + icon so the app appears in your
   start menu (works on KDE, GNOME, Niri, anything that reads
   `~/.local/share/applications/`) and registers `nxm://` so Nexus "Mod
   Manager Download" buttons route to the running daemon.

It does not start the GUI. Launch Gorganizer from your application menu, or
run `./gorganizer.sh launch`.

Re-running `./gorganizer.sh` later is an in-place update: it rebuilds only
when sources changed and refreshes the desktop entry if you moved the clone.

Mod folders live alongside the script: `<clone>/<Game>_Mods/` (e.g.
`~/gorganizer/FalloutNV_Mods/`). The daemon log lands at
`~/.local/state/gorganizer/gorganizerd.log`.

### Manual register / unregister

The default flow handles registration. If you ever need to force-refresh the
desktop entries (`gorganizer.desktop` and the `nxm://` handler) without
launching the app:

```bash
./gorganizer.sh register     # idempotent
./gorganizer.sh unregister   # remove menu entry + nxm handler + icon
```

### Subcommands

| Command | What it does |
|---|---|
| `./gorganizer.sh` | Install or update: build if needed (prompting for deps), register the desktop entry. |
| `./gorganizer.sh launch` | Start the daemon and GUI (what the desktop entry runs). |
| `./gorganizer.sh setup` | Detect distro, install build deps via `sudo $PM`. |
| `./gorganizer.sh doctor` | Check build and runtime dependencies without changing anything. |
| `./gorganizer.sh build [--rebuild]` | Build only. `--rebuild` forces a clean rebuild. |
| `./gorganizer.sh update [--restart]` | Pull the latest `main`, rebuild, re-register. `--restart` bounces a running daemon. |
| `./gorganizer.sh register` | Install menu entry + icon + nxm:// handler. |
| `./gorganizer.sh unregister` | Reverse `register`. |
| `./gorganizer.sh nxm <URI>` | One-shot: forward an `nxm://` URL to the daemon. |
| `./gorganizer.sh import [--from PATH]` | Migrate `*_Mods/` folders from a previous install. |
| `./gorganizer.sh uninstall [--purge]` | Stop the daemon, unregister, delete build artifacts. User data is kept unless `--purge`. |
| `./gorganizer.sh --version` | Print the version. |
| `./gorganizer.sh --help` | Usage. |

### Migrating from a previous install

**From a prior `install.sh` (system install):** detected automatically on
first run. The script prompts to move
`~/.local/share/gorganizer/<gameID>/mods/` → `<clone>/<GameName>_Mods/`. If
you said no the first time, run `./gorganizer.sh import` to revisit.

**From an older `gorganizer.sh` clone:**

```bash
./gorganizer.sh import --from ~/old-gorganizer
```

Walks the source for any known `*_Mods/` folders (Skyrim_Mods, FalloutNV_Mods,
…) and moves them into the current clone after a confirmation prompt.

## Usage

After install, launch from your application menu, or:

```bash
./gorganizer.sh launch
```

The script manages the daemon's lifetime — it spawns `gorganizerd`, waits
for the gRPC socket to bind, then runs the GUI in the foreground. When you
exit the GUI, the daemon shuts down cleanly (it waits on any in-flight
directly-launched Proton processes — the script extender and external tools —
before tearing down the mod hardlink farm; the game itself launches through
Steam, which the daemon does not track).

### Runtime requirements

The normal install flow offers to install the applicable packages; for reference:

- Qt6 (Core, Gui, Widgets, Network) — runtime libraries
- gRPC C++ runtime + libprotobuf
- (optional) `fusermount3` — only used to clean up stale mounts left by
  pre-hardlink-farm versions of gorganizer
- `7z` and `unzip` for archive extraction. 7-Zip handles RAR archives too,
  so `unrar` is not required.
- `protontricks` for installing Proton prefix runtimes such as .NET and VC++
  for managed Windows modding tools
- GStreamer for audio conversion during Tale of Two Wastelands installs

`./gorganizer.sh doctor` reports missing build dependencies and these optional
runtime tools without changing anything.

### Crash recovery

If the daemon dies while a game's mods are deployed, it repairs the game's
`Data/` folder on its next start, asking you to confirm only when the on-disk
state is ambiguous. To recover by hand instead, stop Gorganizer and run:

```bash
./gorganizerctl recover --game skyrimse
```

If recovery reports that it needs confirmation, inspect the listed folder
first, then follow the `recover-confirm` command it prints.
`./gorganizerctl export` and `./gorganizerctl import` back up and restore a
game's mods and profiles while the daemon is running; see
`./gorganizerctl --help`.

## Building manually

If you'd rather drive `make` yourself:

```bash
make all      # generate proto, build gorganizerd + gorganizerctl
make gui      # CMake/Qt6 frontend → build/src/gorganizer
make test     # Go unit tests
make verify   # vet + tests + comment-policy check
make clean    # wipe build artifacts and generated proto
```

Build dependencies (in addition to runtime deps): Go 1.26+, CMake 3.21+,
Ninja or Make, g++ with C++20, `protoc`, `pkg-config`, Qt6 dev headers,
gRPC dev headers.

## Repo layout

- `cmd/gorganizerd/` — daemon entry point
- `cmd/gorganizerctl/` — maintenance CLI: offline crash recovery, plus
  instance export/import against the running daemon
- `cmd/vfs-smoke/`, `cmd/runtime-probe/` — developer diagnostics
- `internal/` — Go packages (daemon services, ipc, vfs, download, transfer, ...)
- `api/proto/` — gRPC service definition
- `src/` — Qt6 GUI (C++)
- `scripts/` — dev tooling (comment-policy checker)
- `resources/icons/` — bundled app icon
- `gorganizer.sh` — single entry point: build, run, register, uninstall
- `cleaner.sh` — developer reset to a first-run state (deletes mods and config)

## License

GPL-3.0. See [LICENSE](LICENSE).
