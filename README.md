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
5. Launches the GUI.

Subsequent runs are zero-friction. The script verifies the desktop entry
each launch and refreshes it automatically if you moved the clone.

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
exit the GUI, the daemon shuts down cleanly (it waits on any in-flight
directly-launched Proton processes — the script extender and external tools —
before tearing down the mod hardlink farm; the game itself launches through
Steam, which the daemon does not track).

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
