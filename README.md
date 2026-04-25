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

One-line install (downloads the latest release):

```bash
curl -fsSL https://raw.githubusercontent.com/parka735/gorganizer/main/install.sh | bash
```

Or from a clone:

```bash
git clone https://github.com/parka735/gorganizer
cd gorganizer
./install.sh
```

Both modes install user-locally to `~/.local/` — no sudo required. They also
register Gorganizer as the system handler for `nxm://` links so Nexus Mods
"Mod Manager Download" buttons work.

### Runtime requirements

The installer checks these before installing and bails with distro-specific
hints if they're missing:

- Qt6 (Core, Gui, Widgets) — runtime libraries, not dev headers
- gRPC C++ runtime
- libprotobuf
- libfuse3 + the `fusermount3` setuid helper
- (optional) `7z`, `unrar`, `unzip` for archive extraction

Tested on Arch, Fedora 39+, Ubuntu 24.04+, openSUSE Tumbleweed. Older distros
(Debian 12, Ubuntu 22.04) ship Qt 6.2 which the binary won't load against —
build from source there for now.

### Install pinned to a specific version

```bash
./install.sh --version v0.1.0
```

### Uninstall

```bash
~/.local/share/gorganizer/uninstall.sh
```

User mods, profiles, and config are preserved. Pass `--purge` to also remove
those.

## Usage

After install, launch from your application menu, or:

```bash
gorganizer
```

The launcher manages the daemon's lifetime — it spawns `gorganizerd`, waits
for the gRPC socket to bind, then runs the GUI in the foreground. When you
exit the GUI, the daemon is asked to shut down cleanly (it waits on any
in-flight Proton launches before unmounting the overlay).

Per-game mod folders live at:

- `~/.local/share/gorganizer/<Game>_Mods/` — installed mode (default)
- `<repo>/<Game>_Mods/` — when running with `GORGANIZER_ROOT` set (dev mode)

Daemon log: `~/.local/state/gorganizer/gorganizerd.log`.

## Building from source

Contributors should use `make`:

```bash
make all      # generate proto, build gorganizerd + gorganizerctl
make gui      # CMake/Qt6 frontend → build/src/gorganizer
make package  # produce a release tarball (mirrors what CI does)
```

Build dependencies (in addition to runtime deps): Go 1.26+, CMake 3.21+,
Ninja or Make, g++ with C++20, `protoc`, `pkg-config`, Qt6 dev headers,
gRPC dev headers, FUSE3 dev headers.

The release pipeline (`.github/workflows/release.yml`) is the canonical
build incantation — when in doubt, read that.

## Cutting a release

Maintainers:

```bash
git tag -a v0.1.0 -m "Initial release"
git push origin v0.1.0
```

The `release` workflow builds the tarball, computes a sha256, attests the
build provenance, and publishes a GitHub Release. Users picked up by
`install.sh` automatically.

## Repo layout

- `cmd/gorganizerd/` — daemon entry point
- `cmd/gorganizerctl/` — offline maintenance CLI (does not talk to the
  daemon; used to recover stale FUSE state)
- `internal/` — Go packages (config, daemon, ipc, vfs, downloads, ...)
- `api/proto/` — gRPC service definition
- `src/` — Qt6 GUI (C++)
- `dist/` — `.desktop` templates
- `scripts/gorganizer-launcher.in` — runtime launcher template
- `install.sh`, `uninstall.sh` — user-facing installers
