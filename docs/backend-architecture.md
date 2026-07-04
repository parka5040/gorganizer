# Gorganizer Backend Architecture

> ⚠️ **PARTLY HISTORICAL — verify against code before relying on this.** The VFS is now a **hardlink farm**, not FUSE/OverlayFS (there is no `go-fuse` dependency; residual FUSE code only detects/unmounts a legacy mount during recovery). The `KnownGames` list here omits `skyrimse`/`ttw`, the RPC list is a small subset of the real ~83, and the `dist/` packaging tree does not exist. The living, ground-truth reference is `docs/local/ARCHITECTURE.md` (gitignored). Treat the code as authoritative.

## Overview

The Gorganizer backend is a Go daemon (`gorganizerd`) that provides a FUSE3-based virtual file system for Bethesda game modding on Linux. It presents a merged, read-only view of the game's original Data directory overlaid with user-installed mods, respecting a configurable priority order. At runtime, mod files are indistinguishable from files physically present in the game's Data directory.

The C++ Qt6 frontend communicates with this daemon via gRPC over a Unix domain socket.

---

## 1. VFS Design

### 1.1 Library: `hanwen/go-fuse/v2`

The backend uses [go-fuse v2](https://github.com/hanwen/go-fuse) (v2.9.0+) with the `fs.InodeEmbedder` API (nodefs). This provides direct access to the FUSE lowlevel inode model, supports FUSE3 natively on Linux, and critically supports **FUSE_PASSTHROUGH** for near-native I/O performance.

Alternatives considered:
- `bazil.org/fuse` — higher-level but less actively maintained, no passthrough support
- Kernel OverlayFS — not available on all systems (confirmed unavailable on the target kernel)

### 1.2 Mount Strategy: Rename-and-Mount

The VFS mounts directly at the game's `Data/` path so that Proton/Wine sees it transparently (Proton maps `z:\` to `/`, so any Linux path is visible to Wine processes).

**Activation sequence:**

1. Verify `Data/` exists and `Data.orig/` does not (safety check)
2. Rename `Data/` to `Data.orig/` (atomic on same filesystem)
3. Create empty `Data/` directory as the FUSE mountpoint
4. Mount the FUSE overlay at `Data/`
5. The overlay uses `Data.orig/` as the base layer (lowest priority)

**Deactivation sequence:**

1. Unmount FUSE (`server.Unmount()`)
2. Remove the empty `Data/` mountpoint (`os.Remove`)
3. Rename `Data.orig/` back to `Data/`

**Crash recovery (run on daemon startup):**

For each known game, check if `Data.orig/` exists without an active FUSE mount at `Data/`. If so, restore by removing the empty `Data/` and renaming `Data.orig/` back.

```go
type MountManager struct {
    GameDataPath  string // e.g., .../Skyrim Special Edition/Data
    BackupSuffix  string // ".orig"
    MountedServer *fuse.Server
}

func (m *MountManager) Activate(tree *MergedTree) error
func (m *MountManager) Deactivate() error
func (m *MountManager) RecoverIfNeeded() error
```

### 1.3 FUSE Node: `OverlayNode`

Each node in the mounted filesystem is an `OverlayNode` that implements `fs.InodeEmbedder`:

```go
type OverlayNode struct {
    fs.Inode
    RealPath string      // For files: the resolved real path on disk
    tree     *MergedTree  // Reference to the merged tree
    VPath    string       // Virtual path relative to mount root
}
```

**Implemented interfaces:**

| Interface | Purpose |
|-----------|---------|
| `fs.NodeLookuper` | Resolve child by name (case-insensitive) |
| `fs.NodeReaddirer` | List directory contents (union of all layers) |
| `fs.NodeGetattrer` | Stat the resolved real file |
| `fs.NodeOpener` | Open the resolved real file with FUSE_PASSTHROUGH |
| `fs.NodeReleaser` | Unregister backing FD on file close |

**File open with FUSE_PASSTHROUGH:**

On `Open()`, the node registers the real backing file with the kernel via `Server.RegisterBackingFd()`. This tells the kernel to bypass userspace entirely for all subsequent `read()`, `splice()`, and `mmap()` calls — the kernel reads directly from the backing file at native speed.

```go
type OverlayFileHandle struct {
    fd        *os.File
    backingId int32
    server    *fuse.Server
}

func (n *OverlayNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
    f, err := os.Open(n.RealPath)
    if err != nil {
        return nil, 0, fs.ToErrno(err)
    }
    bid, err := n.tree.server.RegisterBackingFd(f.Fd())
    if err != nil {
        // Fallback to standard FUSE read if passthrough unavailable
        return &OverlayFileHandle{fd: f, backingId: -1}, fuse.FOPEN_KEEP_CACHE, fs.OK
    }
    return &OverlayFileHandle{fd: f, backingId: bid, server: n.tree.server},
           fuse.FOPEN_KEEP_CACHE | fuse.FOPEN_PASSTHROUGH, fs.OK
}

func (h *OverlayFileHandle) Release(ctx context.Context) syscall.Errno {
    if h.backingId >= 0 {
        h.server.UnregisterBackingFd(h.backingId)
    }
    h.fd.Close()
    return fs.OK
}
```

The mount is read-only — Bethesda games read from `Data/` but write saves and configs to the Proton prefix.

For **directory nodes**, `Readdir` queries the `MergedTree` for all children at that virtual path.

### 1.4 Core Data Structure: `MergedTree`

The `MergedTree` is a precomputed merged view of all layers (base game + enabled mods). It is built once when the VFS is mounted and rebuilt only when the user changes the mod list.

```go
type MergedTree struct {
    mu     sync.RWMutex
    dirs   map[string]map[string]ChildEntry  // vpath -> children
    files  map[string]string                  // vpath -> real file path
    layers []Layer
    server *fuse.Server                       // for passthrough FD registration
}

type Layer struct {
    Name     string // mod name or "__base__"
    RootPath string // absolute path to layer root
    Enabled  bool
}

type ChildEntry struct {
    Name  string // original-case name for directory listings
    IsDir bool
}
```

**Build algorithm:**

1. Walk the base game `Data.orig/` directory recursively. Insert every file and directory into the maps with normalized (lowercased) keys.
2. For each enabled mod in ascending priority order, walk its `Data/` directory tree. For each file, overwrite the entry in `files` (higher priority wins). For directories, merge children into `dirs`.
3. Uses `os.ReadDir` per layer (not `filepath.Walk`) for efficiency.

**Performance characteristics:**
- Build time: ~100ms for 200 mods / 50,000 files
- Lookup: O(1) map access during gameplay
- Concurrency: `sync.RWMutex` — all FUSE operations take read locks during gameplay; write lock taken only during rebuild (between game sessions)

### 1.5 Case-Insensitive Path Handling

Bethesda games running through Wine/Proton expect case-insensitive file access. All map keys in `MergedTree` use `strings.ToLower()` normalized paths. `Lookup` normalizes the requested name before lookup. `ChildEntry.Name` preserves original case for directory listings.

```go
func NormalizePath(p string) string {
    return strings.ToLower(p)
}
```

### 1.6 Performance: FUSE_PASSTHROUGH

The target kernel (6.19-zen) has full FUSE passthrough support:
- `CONFIG_FUSE_PASSTHROUGH=y` — kernel-level passthrough for read/write/mmap/splice
- `CONFIG_FUSE_DAX=y` — direct access for memory-mapped I/O
- `CONFIG_FUSE_IO_URING=y` — async I/O ring support

**How passthrough eliminates overhead:**

Without passthrough, every `read()` on a FUSE file crosses the kernel-userspace boundary twice: kernel → FUSE daemon → kernel → real file → kernel → FUSE daemon → kernel → application. With passthrough, after the initial `Open()`, the kernel reads the backing file directly — the FUSE daemon is completely bypassed for data I/O.

| Setting | Value | Rationale |
|---------|-------|-----------|
| `FOPEN_PASSTHROUGH` | all file opens | Kernel reads backing file directly — zero userspace overhead for I/O |
| `EntryTimeout` | 86400s | 24h — tree is static during a game session |
| `AttrTimeout` | 86400s | Same — attributes don't change |
| `MaxReadAhead` | 131072 | 128KB read-ahead for large BSA sequential reads (up to 1.5GB) |
| `DirectMount` | true | Bypasses fusermount3 helper for faster mount |
| `MaxStackDepth` | 2 | Passthrough stacking limit |

**Performance comparison:**

| Approach | Throughput | Small File Latency | BSA Read Behavior |
|----------|------------|--------------------|--------------------|
| Standard FUSE | ~468 MiB/s | ~50-100us | Every read crosses to userspace |
| FUSE + Passthrough | near-native | ~1-5us | Kernel reads backing file directly |

With passthrough enabled, the FUSE daemon is only involved for `Lookup` (name resolution) and `Open` (one-time FD registration). All subsequent `read()`, `mmap()`, and `splice()` operations hit the real file through the kernel's native VFS — the overlay is invisible to the game.

**Graceful fallback:** If `RegisterBackingFd()` fails (e.g., running on an older kernel without `CONFIG_FUSE_PASSTHROUGH`), the `Open` handler falls back to standard FUSE reads with `FOPEN_KEEP_CACHE`. The VFS still works, just without the passthrough optimization.

---

## 2. Package Structure

```
gorganizer/
├── go.mod
├── go.sum
├── Makefile
├── cmd/
│   └── gorganizerd/
│       └── main.go                  # Daemon entry point + --handle-nxm CLI mode
├── internal/
│   ├── vfs/
│   │   ├── overlay.go               # OverlayNode (FUSE filesystem with passthrough)
│   │   ├── tree.go                  # MergedTree (priority resolution + merged view)
│   │   ├── casefold.go              # Case-insensitive path normalization
│   │   └── mount.go                 # MountManager (rename-and-mount lifecycle)
│   ├── mod/
│   │   ├── mod.go                   # Mod type definition, file scanning
│   │   ├── modlist.go               # modlist.txt parser/writer
│   │   └── conflict.go              # Conflict detection between mods
│   ├── profile/
│   │   └── profile.go               # Profile struct, load/save, switching
│   ├── game/
│   │   ├── game.go                  # Game detection from Steam library
│   │   └── bethesda.go              # Bethesda game registry + script extender tool registry
│   ├── download/
│   │   ├── nxm.go                   # NXM URI parser + game slug mapping
│   │   ├── nexus.go                 # Nexus Mods API client (download URL resolution)
│   │   ├── downloader.go            # HTTP download with progress tracking
│   │   └── installer.go             # Archive extraction + mod structure detection
│   ├── tools/
│   │   ├── tools.go                 # Script extender registry and auto-detection
│   │   └── proton.go                # Proton version detection and game launch
│   ├── config/
│   │   ├── config.go                # Global daemon configuration
│   │   └── paths.go                 # XDG-compliant path resolution
│   ├── ipc/
│   │   ├── server.go                # gRPC server over Unix domain socket
│   │   └── handlers.go              # RPC method implementations
│   └── daemon/
│       └── daemon.go                # Daemon lifecycle, signal handling
├── api/
│   └── proto/
│       └── gorganizer.proto         # gRPC service + message definitions
└── dist/
    ├── gorganizer.desktop           # App launcher
    ├── gorganizer-nxm.desktop       # nxm:// protocol handler
    ├── gorganizerd.service          # systemd user unit
    ├── PKGBUILD                     # Arch Linux package
    ├── gorganizer.spec              # RPM spec
    ├── debian/                      # Debian packaging
    │   ├── control
    │   ├── rules
    │   ├── changelog
    │   └── compat
    └── install.sh                   # Universal install script
```

---

## 3. Mod Storage

### 3.1 Directory Layout

```
~/.local/share/gorganizer/
├── config.json                      # Global daemon configuration
├── downloads/                       # Temporary download staging area
├── mods/
│   └── <GameID>/                    # e.g., skyrimse/
│       ├── SkyUI/
│       │   └── Data/
│       │       ├── SkyUI_SE.esp
│       │       └── Interface/...
│       └── HD Textures/
│           └── Data/
│               └── textures/...
└── profiles/
    └── <GameID>/
        └── <ProfileName>/
            ├── modlist.txt          # Mod priority and enable/disable state
            └── profile.json         # Profile metadata
```

**What the VFS manages:** Each mod's `Data/` subdirectory is overlaid by the FUSE VFS. This covers plugins (.esp/.esm/.esl), BSA archives, textures, meshes, scripts, sounds, and all other files that belong in the game's `Data/` folder.

**What lives directly in the game directory:** Root-level files — script extender loaders (e.g., `skse64_loader.exe`), their core DLLs, and utilities like anti-crash — are placed directly into the game's install directory by the user or the mod installer. Proton/Wine sees the game directory as a normal filesystem path and loads these files natively. No symlinks or VFS needed — they just work. See Section 13 for details.

### 3.2 `modlist.txt` Format

MO2-compatible format. Lines are ordered by priority (first = lowest, last = highest).

```
# Gorganizer modlist — do not edit while daemon is running
+Unofficial Skyrim SE Patch
+SKSE64
+SkyUI
-Optional HD Textures
```

Prefix meanings:
- `+` = enabled
- `-` = disabled

```go
type ModListEntry struct {
    Name    string
    Enabled bool
}

func ParseModList(r io.Reader) ([]ModListEntry, error)
func WriteModList(w io.Writer, entries []ModListEntry) error
```

### 3.3 Mod Type

```go
type Mod struct {
    Name      string   // Directory name = display name
    GameID    string   // e.g., "skyrimse"
    BasePath  string   // Absolute path to mod directory
    DataPath  string   // BasePath + "/Data" (the layer root for VFS)
    Files     []string // Relative file paths within DataPath
    FileCount int
    TotalSize int64
}

func (m *Mod) Scan() error  // Walk DataPath, populate Files/FileCount/TotalSize
```

### 3.4 Conflict Detection

During `MergedTree.Build()`, when a file path already exists from a lower-priority layer, a conflict is recorded:

```go
type FileConflict struct {
    VirtualPath string   // e.g., "textures/sky/sky.dds"
    Winner      string   // Mod that provides this file
    Losers      []string // Overridden mods (lowest priority first)
}

type ConflictMap struct {
    Conflicts map[string]FileConflict
}

func BuildConflictMap(layers []Layer) (*ConflictMap, error)
```

---

## 4. Profile System

Profiles are simple: each profile is a directory containing `modlist.txt` and `profile.json`. Switching profiles means reading a different `modlist.txt` and rebuilding the `MergedTree`.

```go
type Profile struct {
    Name      string    `json:"name"`
    GameID    string    `json:"game_id"`
    CreatedAt time.Time `json:"created_at"`
}

type ProfileManager struct {
    DataDir  string
    profiles map[string]map[string]*Profile // GameID -> Name -> Profile
}

func (pm *ProfileManager) Load(gameID, name string) (*Profile, []ModListEntry, error)
func (pm *ProfileManager) Save(p *Profile, entries []ModListEntry) error
func (pm *ProfileManager) List(gameID string) ([]*Profile, error)
```

---

## 5. IPC: gRPC over Unix Domain Socket

### 5.1 Why gRPC

| Alternative | Issue |
|-------------|-------|
| D-Bus | Session bus dependency, XML introspection overhead, less ergonomic Go libraries |
| Raw Unix socket | Requires designing wire format, versioning, error handling from scratch |
| REST over localhost | HTTP overhead unnecessary for local IPC |

gRPC provides: strong typing via protobuf, streaming support for live status updates, excellent Go and C++ support, code generation for both sides.

### 5.2 Socket Path

`$XDG_RUNTIME_DIR/gorganizer/gorganizer.sock` (e.g., `/run/user/1000/gorganizer/gorganizer.sock`)

### 5.3 Service Definition

```protobuf
syntax = "proto3";
package gorganizer.v1;

service Gorganizer {
  // Game management
  rpc ListGames(ListGamesRequest) returns (ListGamesResponse);
  rpc DetectGames(DetectGamesRequest) returns (DetectGamesResponse);

  // Mod management
  rpc ListMods(ListModsRequest) returns (ListModsResponse);
  rpc GetModInfo(GetModInfoRequest) returns (ModInfo);
  rpc ScanMod(ScanModRequest) returns (ModInfo);

  // Profile management
  rpc ListProfiles(ListProfilesRequest) returns (ListProfilesResponse);
  rpc CreateProfile(CreateProfileRequest) returns (Profile);
  rpc GetModList(GetModListRequest) returns (ModListResponse);
  rpc SetModList(SetModListRequest) returns (SetModListResponse);

  // VFS control
  rpc MountVFS(MountVFSRequest) returns (MountVFSResponse);
  rpc UnmountVFS(UnmountVFSRequest) returns (UnmountVFSResponse);
  rpc GetVFSStatus(GetVFSStatusRequest) returns (VFSStatus);
  rpc RebuildVFS(RebuildVFSRequest) returns (RebuildVFSResponse);

  // Conflict analysis
  rpc GetConflicts(GetConflictsRequest) returns (ConflictsResponse);

  // NXM download handling
  rpc HandleNXM(HandleNXMRequest) returns (HandleNXMResponse);
  rpc GetDownloadProgress(GetDownloadProgressRequest) returns (DownloadProgress);

  // Game launch (with optional script extender)
  rpc LaunchGame(LaunchGameRequest) returns (LaunchGameResponse);
  rpc DetectProton(DetectProtonRequest) returns (DetectProtonResponse);

  // Lifecycle
  rpc Shutdown(ShutdownRequest) returns (ShutdownResponse);

  // Live status streaming
  rpc WatchStatus(WatchStatusRequest) returns (stream StatusEvent);
}
```

Key message types:

```protobuf
message VFSStatus {
  bool mounted = 1;
  string game_id = 2;
  string profile_name = 3;
  string mount_point = 4;
  int32 enabled_mod_count = 5;
  int32 total_file_count = 6;
}

message ModListEntry {
  string mod_name = 1;
  bool enabled = 2;
  int32 priority = 3;
}

message FileConflict {
  string virtual_path = 1;
  string winning_mod = 2;
  repeated string losing_mods = 3;
}

message StatusEvent {
  oneof event {
    VFSStatus vfs_status = 1;
    DownloadProgress download_progress = 2;
    string error = 3;
    string info = 4;
  }
}

// NXM downloads
message HandleNXMRequest {
  string nxm_uri = 1;
}

message HandleNXMResponse {
  string download_id = 1;
}

message DownloadProgress {
  string download_id = 1;
  string mod_name = 2;
  int64 bytes_downloaded = 3;
  int64 bytes_total = 4;
  enum Status {
    PENDING = 0;
    DOWNLOADING = 1;
    EXTRACTING = 2;
    INSTALLING = 3;
    COMPLETE = 4;
    FAILED = 5;
  }
  Status status = 5;
  string error = 6;
}

// Game launch
message LaunchGameRequest {
  string game_id = 1;
  bool use_tool = 2;
}

message LaunchGameResponse {
  int32 pid = 1;
}

message DetectProtonResponse {
  repeated ProtonVersion versions = 1;
}

message ProtonVersion {
  string name = 1;
  string path = 2;
}
```

### 5.4 Server Implementation

```go
type Server struct {
    socketPath string
    grpcServer *grpc.Server
    daemon     *daemon.Daemon
}

func NewServer(socketPath string, d *daemon.Daemon) *Server
func (s *Server) Start() error  // Listen on Unix socket, serve gRPC
func (s *Server) Stop()         // Graceful stop
```

---

## 6. Daemon Lifecycle

### 6.1 Daemon Struct

```go
type Daemon struct {
    config      *config.Config
    profileMgr  *profile.ProfileManager
    mountMgrs   map[string]*vfs.MountManager  // GameID -> MountManager
    currentTree *vfs.MergedTree
    downloadMgr *download.Manager
    toolMgr     *tools.Manager
    ipcServer   *ipc.Server
    shutdownCh  chan struct{}
    wg          sync.WaitGroup
}
```

### 6.2 Startup Sequence

1. Parse CLI flags (`--config`, `--socket-path`, `--log-level`, `--handle-nxm <uri>`)
2. If `--handle-nxm`: forward URI to running daemon via gRPC, then exit
3. Load configuration from `~/.config/gorganizer/config.json`
4. Run crash recovery for all known games (restore Data/ dirs if needed)
5. Create `ProfileManager`, load game definitions
6. Initialize download manager and tool manager
7. Start gRPC IPC server on Unix domain socket
8. Register signal handlers: `SIGTERM`, `SIGINT` → graceful shutdown
9. Block on signal or shutdown channel

### 6.3 Shutdown Sequence

1. Signal received or `Shutdown` RPC called
2. Stop accepting new gRPC calls
3. Cancel any active downloads
4. For each active mount: `MountManager.Deactivate()` (unmount FUSE, restore Data/)
5. Stop gRPC server
6. Exit

### 6.4 Mount Flow (triggered by `MountVFS` RPC)

1. Load profile's `modlist.txt`, parse into `[]ModListEntry`
2. Resolve each enabled mod name to its `DataPath` on disk
3. Build layer list: Layer 0 = `Data.orig/` (base), Layers 1..N = enabled mods by priority
4. Call `MergedTree.Build()` — walk all layers, construct merged maps
5. Call `MountManager.Activate()` — rename `Data/` → `Data.orig/`, create mountpoint, mount FUSE with passthrough
6. Return `VFSStatus` to frontend

---

## 7. Game Detection

```go
var KnownGames = []GameDefinition{
    {ID: "morrowind",  Name: "The Elder Scrolls III: Morrowind",            SteamAppID: 22320,  DataSubpath: "Data"},
    {ID: "oblivion",   Name: "The Elder Scrolls IV: Oblivion",             SteamAppID: 22330,  DataSubpath: "Data"},
    {ID: "skyrim",     Name: "The Elder Scrolls V: Skyrim",                SteamAppID: 72850,  DataSubpath: "Data"},
    {ID: "skyrimse",   Name: "The Elder Scrolls V: Skyrim Special Edition",SteamAppID: 489830, DataSubpath: "Data"},
    {ID: "fallout3",   Name: "Fallout 3",                                  SteamAppID: 22370,  DataSubpath: "Data"},
    {ID: "falloutnv",  Name: "Fallout: New Vegas",                         SteamAppID: 22380,  DataSubpath: "Data"},
    {ID: "fallout4",   Name: "Fallout 4",                                  SteamAppID: 377160, DataSubpath: "Data"},
    {ID: "starfield",  Name: "Starfield",                                  SteamAppID: 1716740,DataSubpath: "Data"},
}

func DetectInstalledGames() ([]DetectedGame, error) {
    // 1. Parse ~/.local/share/Steam/steamapps/libraryfolders.vdf
    // 2. For each library, scan appmanifest_*.acf files
    // 3. Match appid against KnownGames
    // 4. Read installdir from manifest (never hardcode)
    // 5. Verify Data/ directory exists
}
```

---

## 8. Configuration

### 8.1 XDG Paths

```go
func DataDir() string    // $XDG_DATA_HOME/gorganizer   (~/.local/share/gorganizer)
func ConfigDir() string  // $XDG_CONFIG_HOME/gorganizer  (~/.config/gorganizer)
func RuntimeDir() string // $XDG_RUNTIME_DIR/gorganizer  (/run/user/<uid>/gorganizer)
func SocketPath() string // RuntimeDir()/gorganizer.sock
```

### 8.2 Global Config

```go
type Config struct {
    Games       map[string]GameConfig `json:"games"`
    LogLevel    string                `json:"log_level"`
    NexusAPIKey string                `json:"nexus_api_key"`
}

type GameConfig struct {
    Name        string `json:"name"`
    InstallPath string `json:"install_path"`
    DataSubpath string `json:"data_subpath"`
    SteamAppID  int    `json:"steam_app_id"`
    Tool        string `json:"tool"`         // e.g., "skse64"
    ToolExe     string `json:"tool_exe"`     // e.g., "skse64_loader.exe"
    ProtonPath  string `json:"proton_path"`  // auto-detected or manual
}
```

---

## 9. Dependencies

```
module github.com/user/gorganizer

go 1.26

require (
    github.com/hanwen/go-fuse/v2 v2.9.0
    github.com/bodgit/sevenzip v1.6.0
    google.golang.org/grpc v1.72.0
    google.golang.org/protobuf v1.36.6
)
```

Toolchain:
- `protoc` (v34.1 available on system)
- `protoc-gen-go` — `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`
- `protoc-gen-go-grpc` — `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`

Runtime:
- `p7zip` or `7z` CLI — fallback for 7z archives the Go library cannot handle
- `unrar` CLI — RAR archive extraction

---

## 10. Data Flow Diagrams

### 10.1 Mount VFS

```
Frontend                     gRPC                    Daemon
   |                          |                        |
   |--- MountVFS(game,prof) ->|                        |
   |                          |--- load modlist.txt -->|
   |                          |                        |--- resolve mod Data/ paths
   |                          |                        |--- build MergedTree
   |                          |                        |--- rename Data -> Data.orig
   |                          |                        |--- mkdir Data, FUSE mount
   |                          |<-- VFSStatus ----------|
   |<-- mounted status -------|                        |
```

### 10.2 Game File Access (FUSE_PASSTHROUGH)

```
Game (Proton)      Kernel FUSE       OverlayNode        Kernel VFS (direct)
    |                  |                 |                      |
    |-- open tex.dds ->|                 |                      |
    |                  |-- Lookup ------>|                      |
    |                  |                 |-- resolve in tree -->|
    |                  |<-- backingId ---|  RegisterBackingFd   |
    |                  |-- PASSTHROUGH --|--------------------->|
    |<- read (native) -|  (no userspace, kernel reads backing file directly)  |
```

After the initial `Open`, the FUSE daemon is **never involved** in reads. The kernel handles all I/O directly against the backing file. BSA files, textures, meshes — everything reads at native speed.

### 10.3 NXM Download Flow

```
Browser        gorganizerd CLI     Daemon             Nexus API
   |                |                 |                    |
   |-- nxm://... -->|                 |                    |
   |                |-- HandleNXM --->|                    |
   |                |<-- download_id -|                    |
   |                |  (CLI exits)    |                    |
   |                                  |-- GET download_link ->|
   |                                  |<-- CDN URL ----------|
   |                                  |-- download archive -->|
   |                                  |-- detect structure -->|
   |                                  |-- extract to mods/ -->|
   |                                  |-- update modlist.txt  |
   |                                  |-- notify frontend     |
```

### 10.4 Script Extender Launch

```
Frontend                     gRPC                    Daemon
   |                          |                        |
   |--- LaunchGame(game, +--->|                        |
   |      use_tool=true)      |                        |
   |                          |--- detect proton ----->|
   |                          |                        |--- find loader exe in mods
   |                          |                        |--- set STEAM_COMPAT_DATA_PATH
   |                          |                        |--- set SteamAppId, SteamGameId
   |                          |                        |--- proton waitforexitandrun
   |                          |                        |       skse64_loader.exe
   |                          |<-- pid ----------------|
   |<-- launched -------------|                        |
```

### 10.5 Unmount VFS

```
Frontend                     gRPC                    Daemon
   |                          |                        |
   |--- UnmountVFS(game) ---->|                        |
   |                          |--- unmount FUSE ------>|
   |                          |                        |--- rmdir Data (mountpoint)
   |                          |                        |--- rename Data.orig -> Data
   |                          |<-- success ------------|
   |<-- unmounted status -----|                        |
```

---

## 11. Design Decisions

### Why read-only FUSE?

Bethesda games write saves and INI configs to `%USERPROFILE%\Documents\My Games\...` (via Proton, this maps to the Wine prefix at `steamapps/compatdata/<appid>/`). They do not write to `Data/` during gameplay. A read-only mount is simpler, safer, and avoids the write-to-which-layer problem.

### Why FUSE_PASSTHROUGH instead of standard FUSE?

Standard FUSE copies all file data through userspace (kernel → daemon → kernel). For a game loading hundreds of textures and 1.5GB BSA files, this overhead is measurable. FUSE_PASSTHROUGH registers backing file descriptors with the kernel so all I/O bypasses the daemon entirely. The result is indistinguishable from native filesystem performance.

### Why precomputed tree?

On-demand resolution would require N stat calls per file access (N = number of enabled mods). With 200 mods and thousands of file accesses during game startup, this would be catastrophic. The precomputed tree gives O(1) map lookups.

### Why rename-and-mount?

Proton/Wine is fragile with symlinks — some games resolve symlinks to canonical paths, some don't follow them. Mounting directly at the expected `Data/` path eliminates all symlink issues. The rename is atomic and recoverable.

### Why not virtualize root-level files?

Root-level files (script extender exes, anti-crash DLLs) live directly in the game directory. Proton/Wine sees the game directory as a native Linux path — any file placed there is visible to the Windows process. These files are always-on (you don't toggle your script extender), few in number, and loaded by the PE loader which handles them natively. Virtualizing them would add complexity for zero benefit. The VFS focuses exclusively on `Data/` where the volume of files and the need for priority-based overrides justifies the overlay.

### Why gRPC over D-Bus?

The proto definitions serve as the API contract for both Go and C++. Streaming RPCs provide a natural way to push status updates (download progress, mount events). No dependency on session bus availability. Simpler testing (grpcurl).

### Thread safety model

The `MergedTree` is read-heavy, write-rare. During gameplay, all FUSE operations take `RLock()`. Rebuilds take `Lock()`, but only happen when the user explicitly changes the mod list (not during gameplay). Zero lock contention during game file access.

---

## 12. NXM Link Handler

### 12.1 Protocol Registration

A `.desktop` file registers Gorganizer as the handler for `nxm://` URIs:

**`dist/gorganizer-nxm.desktop`:**
```ini
[Desktop Entry]
Type=Application
Name=Gorganizer NXM Handler
Comment=Nexus Mods download handler for Gorganizer
Exec=gorganizerd --handle-nxm %u
Terminal=false
Categories=Game;
NoDisplay=true
MimeType=x-scheme-handler/nxm;
```

Registered via `xdg-mime default gorganizer-nxm.desktop x-scheme-handler/nxm`. The setup wizard handles this during first boot.

### 12.2 NXM URI Format

```
nxm://skyrimspecialedition/mods/12345/files/67890?key=abc123&expires=1234567890
      ├─ game slug         ├─ mod ID       ├─ file ID  ├─ auth params
```

Game slug to GameID mapping:

| NXM Slug | GameID |
|----------|--------|
| `skyrimspecialedition` | skyrimse |
| `skyrim` | skyrim |
| `newvegas` | falloutnv |
| `fallout3` | fallout3 |
| `fallout4` | fallout4 |
| `oblivion` | oblivion |
| `morrowind` | morrowind |
| `starfield` | starfield |

### 12.3 CLI Entry Point

When invoked as `gorganizerd --handle-nxm <uri>`:
1. Parse the NXM URI
2. Connect to the running daemon's gRPC socket
3. Send `HandleNXM(uri)` RPC
4. Exit immediately (the daemon handles the download asynchronously)

If no daemon is running, start one in the background first.

### 12.4 Download Pipeline

**Flow:**
1. **Parse** NXM URI → game slug, mod ID, file ID, auth key
2. **Map** game slug to internal GameID
3. **Resolve download URL** via Nexus Mods API: `GET /v1/games/{game}/mods/{mod_id}/files/{file_id}/download_link.json?key={key}&expires={expires}` (requires API key stored in config)
4. **Download** archive to `~/.local/share/gorganizer/downloads/` with progress tracking
5. **Detect archive type** by magic bytes (7z, zip, rar)
6. **Extract** archive to temp directory
7. **Detect mod structure**: find the Data/ root inside the archive
   - Flat: files directly at archive root or in a single top-level directory
   - BAIN: multiple numbered directories (00 Core, 01 Optional, etc.)
   - FOMOD: `fomod/` directory with ModuleConfig.xml (basic support: install the default option)
8. **Classify files**: Data/ files go to mod storage, root-level files (exes, core DLLs for script extenders) are copied directly to the game directory
9. **Install** Data/ files to `mods/<GameID>/<ModName>/Data/...`
10. **Add** to active profile's modlist.txt as enabled (highest priority)
11. **Notify** frontend via `WatchStatus` stream with `DownloadProgress` events

**Archive extraction dependencies:**
- `archive/zip` — Go stdlib
- `github.com/bodgit/sevenzip` — 7z support (most Nexus mods use 7z)
- RAR: call `unrar` CLI as fallback (Go RAR libraries are limited)

**Nexus API key:** Stored in `~/.config/gorganizer/config.json` as `nexus_api_key`. Users obtain this from their Nexus Mods account settings page. The frontend prompts for it in a settings dialog.

---

## 13. Script Extender Support

### 13.1 Scope

Only script extenders — the loaders that inject into the game process at startup:

| Game | Extender | Loader Executable |
|------|----------|-------------------|
| Skyrim SE | SKSE64 | `skse64_loader.exe` |
| Skyrim | SKSE | `skse_loader.exe` |
| Fallout NV | xNVSE | `nvse_loader.exe` |
| Fallout 3 | FOSE | `fose_loader.exe` |
| Fallout 4 | F4SE | `f4se_loader.exe` |
| Oblivion | OBSE | `obse_loader.exe` |
| Starfield | SFSE | `sfse_loader.exe` |

### 13.2 Root-Level Files: Direct Placement

Script extenders have two kinds of files:
- **Data/ files** (scripts, SKSE plugins in `Data/SKSE/Plugins/`, etc.) — handled by the existing FUSE VFS overlay, fully managed
- **Root-level files** (loader exe, core DLLs) — placed directly in the game's install directory

Root-level files do NOT need any virtualization. Proton/Wine maps the entire Linux filesystem via the `z:\` drive. Any file physically present in the game directory is visible to the Windows process — the PE loader, DLL search paths, everything works natively. This includes:

- Script extender loaders (`skse64_loader.exe`, `nvse_loader.exe`, etc.)
- Script extender core DLLs (`skse64_1_5_97.dll`, `nvse_1_4.dll`, etc.)
- Utility DLLs (anti-crash, stutter fix, etc.)

**Installation:** When a mod download (NXM or manual) contains root-level files, the installer copies them directly to the game directory. The user can also drag-and-drop these files manually. No symlinks, no tracking — they persist in the game directory across mount/unmount cycles.

**Why not virtualize root-level files?** There's no need. These files are typically always-on (you always want your script extender and anti-crash). They're few in number (3-5 files per game). And Proton loads them natively from the game directory without any special handling. Virtualizing them would add complexity for no benefit.

### 13.3 Launch Mechanism

When the user clicks "Run" with a script extender configured, the backend launches via Proton directly instead of through Steam's `steam://rungameid/`.

**Required environment variables** (verified against the Proton script):

| Variable | Value | Purpose |
|----------|-------|---------|
| `STEAM_COMPAT_DATA_PATH` | `steamapps/compatdata/<appid>` | Wine prefix location |
| `STEAM_COMPAT_CLIENT_INSTALL_PATH` | Steam root directory | Steam integration |
| `SteamAppId` | App ID (e.g., `489830`) | Game identification |
| `SteamGameId` | App ID (same) | Logging and compat config |
| `WINEDLLOVERRIDES` | `<extender-dll>=n,b;...` | Force-load native script-extender DLLs. Without this, Wine loads its own stub in place of `nvse_1_4.dll` / `skse64_*.dll` / etc., the hook never fires, and the game silently runs without the extender — classic "boots past splash but menus fail" symptom. The daemon scans the game dir for DLLs matching each tool's `DllPrefixes` (e.g. `nvse_`, `skse64_`) plus explicit extras (xNVSE's `d3dx9_38.dll`) and builds the override string at launch time so version bumps don't break things. |

**Do NOT write a `steam_appid.txt` into the game dir.** Earlier builds of gorganizer did this on the theory that script-extender loaders want it — they don't, when launched via Proton. The Steam client is already running in the background and provides the app ID via the `SteamAppId` / `SteamGameId` env vars. A local `steam_appid.txt` instead binds Steam's DRM to the file (Valve's documented "developer testing outside Steam" mode), which conflicts with the live Steam session and results in Steam reporting the game as "running" while no game window ever appears. The launcher now removes a legacy steam_appid.txt automatically if its content matches exactly what an older gorganizer wrote.

**Launch command:**
```bash
STEAM_COMPAT_DATA_PATH=~/.local/share/Steam/steamapps/compatdata/489830 \
STEAM_COMPAT_CLIENT_INSTALL_PATH=~/.local/share/Steam \
SteamAppId=489830 \
SteamGameId=489830 \
~/.local/share/Steam/steamapps/common/Proton\ 9.0\ \(Beta\)/proton \
  waitforexitandrun \
  ~/.local/share/Steam/steamapps/common/Skyrim\ Special\ Edition/skse64_loader.exe
```

Uses the `waitforexitandrun` verb, which waits for any existing Wine server to shut down before launching the loader. This prevents prefix conflicts.

### 13.4 Tool Registry and Auto-Detection

```go
var KnownTools = map[string]ToolDefinition{
    "skse64": {Name: "SKSE64", LoaderExe: "skse64_loader.exe", GameIDs: []string{"skyrimse"}},
    "skse":   {Name: "SKSE",   LoaderExe: "skse_loader.exe",   GameIDs: []string{"skyrim"}},
    "xnvse":  {Name: "xNVSE",  LoaderExe: "nvse_loader.exe",   GameIDs: []string{"falloutnv"}},
    "fose":   {Name: "FOSE",   LoaderExe: "fose_loader.exe",   GameIDs: []string{"fallout3"}},
    "f4se":   {Name: "F4SE",   LoaderExe: "f4se_loader.exe",   GameIDs: []string{"fallout4"}},
    "obse":   {Name: "OBSE",   LoaderExe: "obse_loader.exe",   GameIDs: []string{"oblivion"}},
    "sfse":   {Name: "SFSE",   LoaderExe: "sfse_loader.exe",   GameIDs: []string{"starfield"}},
}
```

**Auto-detection:** When a mod containing a known loader exe is enabled, the daemon auto-configures the tool for that game. When the mod is disabled, the tool configuration is cleared.

**Proton version detection:** Scan `steamapps/common/Proton*/proton` for available versions. Default to the highest version number. User can override in game config.

---

## 14. Cross-Distro Installer

### 14.1 Installed File Layout (FHS-compliant)

```
/usr/bin/gorganizer                                    — Qt6 frontend binary
/usr/bin/gorganizerd                                   — Go daemon binary
/usr/lib/systemd/user/gorganizerd.service              — systemd user service
/usr/share/applications/gorganizer.desktop             — app launcher
/usr/share/applications/gorganizer-nxm.desktop         — nxm:// protocol handler
/usr/share/icons/hicolor/256x256/apps/gorganizer.png   — app icon
/usr/share/licenses/gorganizer/LICENSE                 — license
```

### 14.2 Runtime Dependencies

| Dependency | Debian/Ubuntu | Fedora/RHEL | Arch |
|-----------|---------------|-------------|------|
| Qt6 Widgets | `libqt6widgets6` | `qt6-qtbase` | `qt6-base` |
| FUSE3 | `libfuse3-3`, `fuse3` | `fuse3-libs`, `fuse3` | `fuse3` |
| 7-Zip | `p7zip-full` | `p7zip-plugins` | `p7zip` |

Build-time only: `cmake`, `g++`, `go`, `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`, Qt6 dev headers, libfuse3-dev.

### 14.3 Arch Linux — PKGBUILD

```bash
pkgname=gorganizer
pkgver=0.1.0
pkgrel=1
pkgdesc="Native Linux mod organizer for Bethesda games"
arch=('x86_64')
license=('GPL')
depends=('qt6-base' 'fuse3' 'p7zip')
makedepends=('cmake' 'go' 'protobuf')

build() {
    cd "$srcdir/$pkgname-$pkgver"
    go build -o gorganizerd ./cmd/gorganizerd
    cmake -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr
    cmake --build build
}

package() {
    cd "$srcdir/$pkgname-$pkgver"
    install -Dm755 gorganizerd "$pkgdir/usr/bin/gorganizerd"
    install -Dm755 build/src/gorganizer "$pkgdir/usr/bin/gorganizer"
    install -Dm644 dist/gorganizer.desktop "$pkgdir/usr/share/applications/gorganizer.desktop"
    install -Dm644 dist/gorganizer-nxm.desktop "$pkgdir/usr/share/applications/gorganizer-nxm.desktop"
    install -Dm644 dist/gorganizerd.service "$pkgdir/usr/lib/systemd/user/gorganizerd.service"
}
```

### 14.4 Debian/Ubuntu — .deb

Standard `dist/debian/` directory with `control`, `rules`, `changelog`, `compat`. Key fields in `control`:
```
Package: gorganizer
Depends: libqt6widgets6 (>= 6.0), fuse3, libfuse3-3, p7zip-full
Build-Depends: cmake (>= 3.21), g++ (>= 13), golang (>= 1.22), qt6-base-dev, libfuse3-dev, protobuf-compiler
```

### 14.5 RPM — .spec

Standard `dist/gorganizer.spec` with:
```
Requires: qt6-qtbase, fuse3-libs, fuse3, p7zip-plugins
BuildRequires: cmake >= 3.21, gcc-c++, golang, qt6-qtbase-devel, fuse3-devel, protobuf-compiler
```

### 14.6 Universal Install Script

`dist/install.sh` for users not on a supported package manager:
1. Checks for required build tools
2. Builds both binaries
3. Copies to `/usr/local/bin/`
4. Installs .desktop files to `~/.local/share/applications/`
5. Registers NXM handler via `xdg-mime`
6. Runs `update-desktop-database`

### 14.7 Post-Install

All package formats trigger:
1. `update-desktop-database` — refresh desktop file cache
2. NXM handler registration deferred to the setup wizard (user-level, not system-level)
3. Optional: `systemctl --user enable gorganizerd.service` for auto-start

---

## 15. Implementation Phases

**Phase 1: Core VFS with passthrough**
1. `casefold.go` — path normalization
2. `tree.go` — MergedTree build algorithm
3. `overlay.go` — OverlayNode FUSE implementation with FUSE_PASSTHROUGH
4. `mount.go` — MountManager lifecycle
5. `main.go` — minimal daemon for testing

**Phase 2: Mod management**
1. `mod.go` — Mod type and scanning
2. `modlist.go` — parser/writer
3. `conflict.go` — conflict detection
4. `profile.go` — profile management
5. `paths.go` / `config.go` — XDG and configuration

**Phase 3: IPC**
1. `gorganizer.proto` — service definition (all RPCs)
2. Generate Go code
3. `server.go` / `handlers.go` — gRPC implementation
4. Wire into `main.go`

**Phase 4: NXM downloads**
1. `nxm.go` — URI parser and game slug mapping
2. `nexus.go` — Nexus Mods API client
3. `downloader.go` — HTTP download with progress
4. `installer.go` — archive extraction and mod structure detection
5. `--handle-nxm` CLI entry point

**Phase 5: Script extender support**
1. `tools.go` — tool registry and auto-detection
2. `proton.go` — Proton detection and launch with correct env vars
3. `LaunchGame` RPC handler

**Phase 6: Packaging**
1. `dist/` files — .desktop entries, systemd service, icon
2. PKGBUILD, .spec, debian/ packaging
3. `install.sh` universal script

**Phase 7: Integration testing**
1. End-to-end test: mount VFS with mods, launch Skyrim SE through Proton
2. NXM download test: click link on Nexus, verify mod installed
3. SKSE test: launch via script extender, verify game loads with SKSE
