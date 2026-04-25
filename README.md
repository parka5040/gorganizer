# Gorganizer

Native Linux mod organizer for Bethesda games. A Go gRPC daemon
(`gorganizerd`) backs a Qt6 GUI; on activation, it merges your enabled mods
into the game's `Data/` folder via a hardlink farm. Built as a Linux-native
alternative to Mod Organizer 2 — no Wine, no Protontricks for the manager
itself.

**Status:** early. Targets Skyrim SE, Fallout New Vegas, Fallout 3, and
similar Data/-folder Bethesda games. Oblivion Remastered (Unreal) is out of
scope.

## Install

```bash
git clone https://github.com/parka5040/gorganizer ~/gorganizer
cd ~/gorganizer
./gorganizer.sh
```

The clone path is up to you — `~/Apps/gorganizer` works just as well. The
script's first run detects your distro (Arch, Debian/Ubuntu, Fedora, openSUSE),
prompts you to install the build dependencies through the system package
manager, builds the daemon and Qt6 GUI in-tree, then launches it. Subsequent
runs are zero-friction.

Mod folders live alongside the script: `<clone>/<Game>_Mods/` (e.g.
`~/gorganizer/FalloutNV_Mods/`). The daemon log lands at
`~/.local/state/gorganizer/gorganizerd.log`.

### Register the system menu and Nexus link handler

```bash
./gorganizer.sh register
```

Installs `gorganizer.desktop` (so Gorganizer shows up in your application
menu), copies the icon, and registers `gorganizer-nxm.desktop` as the system
handler for `nxm://` links — Nexus Mods "Mod Manager Download" buttons
forward to the running daemon.

`register` is idempotent. Re-run it after moving the clone directory; the
desktop entries are pinned to absolute paths.

### Subcommands

| Command | What it does |
|---|---|
| `./gorganizer.sh` | Build if needed (prompting for deps), then run. |
| `./gorganizer.sh setup` | Detect distro, install build deps via `sudo $PM`. |
| `./gorganizer.sh build [--rebuild]` | Build only. `--rebuild` forces a clean rebuild. |
| `./gorganizer.sh register` | Install menu entry + icon + nxm:// handler. |
| `./gorganizer.sh unregister` | Reverse `register`. |
| `./gorganizer.sh nxm <URI>` | One-shot: forward an `nxm://` URL to the daemon. |
| `./gorganizer.sh import [--from PATH]` | Migrate `*_Mods/` folders from a previous install. |
| `./gorganizer.sh uninstall [--purge]` | Stop daemon, unregister, prompt for user data + system packages. |
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

After install, launch from your application menu (if you ran `register`), or
just:

```bash
./gorganizer.sh
```

The script manages the daemon's lifetime — it spawns `gorganizerd`, waits
for the gRPC socket to bind, then runs the GUI in the foreground. When you
exit the GUI, the daemon shuts down cleanly (it waits on any in-flight Proton
launches before unmounting the overlay).

### Runtime requirements

`./gorganizer.sh setup` will install these for you, but for reference:

- Qt6 (Core, Gui, Widgets) — runtime libraries
- gRPC C++ runtime + libprotobuf
- libfuse3 + the `fusermount3` setuid helper
- (optional) `7z`, `unrar`, `unzip` for archive extraction

## Building manually

If you'd rather drive `make` yourself:

```bash
make all      # generate proto, build gorganizerd + gorganizerctl
make gui      # CMake/Qt6 frontend → build/src/gorganizer
make test     # Go unit tests
make clean    # wipe build artifacts and generated proto
```

Build dependencies (in addition to runtime deps): Go 1.26+, CMake 3.21+,
Ninja or Make, g++ with C++20, `protoc`, `pkg-config`, Qt6 dev headers,
gRPC dev headers, FUSE3 dev headers.

## Repo layout

- `cmd/gorganizerd/` — daemon entry point
- `cmd/gorganizerctl/` — offline maintenance CLI (does not talk to the
  daemon; used to recover stale FUSE state)
- `internal/` — Go packages (config, daemon, ipc, vfs, downloads, ...)
- `api/proto/` — gRPC service definition
- `src/` — Qt6 GUI (C++)
- `resources/icons/` — bundled app icon
- `gorganizer.sh` — single entry point: build, run, register, uninstall
