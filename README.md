# Gorganizer

Gorganizer helps you install and manage game mods on Linux without running the manager through Wine. It supports Steam installs of Morrowind, Oblivion, Oblivion Remastered, Skyrim, Skyrim Special Edition, Fallout 3, Fallout: New Vegas, Fallout 4, Starfield, Tale of Two Wastelands (TTW), and Stardew Valley with SMAPI. For Stardew Valley, you need the native Linux Steam build; the Windows/Proton build and GOG copies are not supported.

## Install

You need a Steam install of a supported game. TTW needs both Fallout 3 and Fallout: New Vegas. To build Gorganizer from source, you also need Git, Go 1.26.2 or newer, CMake 3.21 or newer, a C++20 compiler, Ninja or Make, `pkg-config`, the protobuf compiler, and Qt6 and gRPC development files. On Arch, Debian/Ubuntu, Fedora, and openSUSE, the install script offers to install missing build packages through your package manager. It may also offer optional tools for specific features. You do not need to install `7z` or `unzip` separately to extract ordinary mod archives.

Open a terminal and run:

```bash
git clone https://github.com/parka5040/gorganizer ~/gorganizer
cd ~/gorganizer
./gorganizer.sh
```

You can choose another folder instead of `~/gorganizer`. The script builds Gorganizer and adds an application-menu entry and an `nxm://` handler for Nexus Mods links. Both use a launcher that stays at the same location if you rebuild Gorganizer. Installation does **not** open the window.

Open **Gorganizer** from your application menu, or run `./gorganizer.sh launch` from the folder you cloned. Running `./gorganizer.sh` again is safe: it checks the build and refreshes the menu entry when needed.

## First start

The setup wizard looks for games in all your Steam libraries, including libraries used by Flatpak or Snap Steam. Select the games you want to manage. If Steam does not list a game, choose **Locate game…** and select that game's program file.

A Nexus Mods personal API key is optional during setup. To use Nexus downloads, paste your key on the **Nexus Mods API Key** page and choose **Save Nexus key**. The wizard links to [your API key page on Nexus Mods](https://www.nexusmods.com/users/myaccount?tab=api+access). You can also add it later under **Tools → Settings...**. Choose **Finish** to save your selected games.

## Everyday use

### Install mods

- Choose **Mod Manager Download** on a Nexus Mods file page. Gorganizer adds it to the **Downloads** tab. Registered links can open Gorganizer even when its window is closed.
- Double-click a downloaded archive in **Downloads** to install it. You can also use **File → Install Mod...**, or drop a `.zip`, `.7z`, or `.rar` archive onto the Gorganizer window. Select a game first.
- If a mod with the same name already exists, choose **Replace** to install an update while keeping its settings, **Merge into existing** to keep old files and add the new ones, or **Install as a separate mod…** to keep both. You can also cancel.

Tick mods in the mod list to enable them. Drag them to change their order; mods lower in the list take priority when they contain the same file. For games with plugins, use the **Plugins** tab to enable and order them. To drag plugins, first sort by **Index** in ascending order.

**Apply Changes** updates the game's mod view after you change your choices. You can also choose **Run**: launching applies pending changes automatically. If **Apply Changes** is not showing, there are no pending changes to apply.

The status at the bottom of the window tells you what is happening:

- **Mods active** — your selected mods are in the game folder.
- **Changes pending** — choose **Apply Changes** or launch to use your latest choices.
- **Mods inactive** — no mods are active for this game.
- **Connection lost** — the background service disconnected; mod status is unknown.
- **Paused for Steam** — mods are paused until you finish the Steam update or verification.
- **Waiting for the game to close** — mods were left active because the game may still be running.
- **Needs your decision** — review an interrupted mod change before Gorganizer continues.
- **Steam changed the game** — Steam changed files while mods were active; open **Tools → Steam Update Help…**.

### Profiles

A profile saves a set of mod and plugin choices. Use **+** beside **Profile:** to create one, or **Copy** to copy the current profile, including its order and settings. Pick a profile from the same menu to use it.

## Where your files are

By default, your files are in your home folder, not in the cloned source folder:

| What | Location |
|---|---|
| Mods | `~/.local/share/gorganizer/<game>/mods/` |
| Downloads | `~/.local/share/gorganizer/<game>/mods/Downloads/` |
| Profiles | `~/.local/share/gorganizer/<game>/profiles/` |
| Settings | `~/.config/gorganizer/` |
| Background-service log | `~/.local/state/gorganizer/daemon.log` |

`<game>` is a short ID, such as `skyrimse` or `stardewvalley`. If you have set `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, or `XDG_STATE_HOME`, Gorganizer uses those locations instead of the defaults above.

On launch, Gorganizer tries to move old `*_Mods` folders from the cloned source folder to your personal data folder. This is a one-time move. If it cannot start the move, it warns you, uses the old mod folders for that session, and tries again the next time. If a move was interrupted, it tries to finish it before opening; if that fails, it stops and tells you what went wrong. Do not delete the source folder while it still contains your mods.

### How it works

Gorganizer builds a **hardlink farm**: a game folder assembled from your enabled mods, with the original game files kept aside. You do not need to keep the window open while playing.

## While a game is running

Your mods stay active while the game runs, even if you close Gorganizer. Gorganizer will not replace or remove the active mod view until the game has closed. If a change cannot be applied yet, close the game and try again.

## Steam updates and “Verify integrity”

Before you ask Steam to update or verify a game, choose **Tools → Pause Mods for a Steam Update…**. This puts the original game files back so Steam can work. When Steam is done, open **Tools → Steam Update Help…** and choose **Steam Finished**.

If Steam changes files while mods are active, Gorganizer shows **Steam changed the game**. Open **Tools → Steam Update Help…** and choose **Pause Mods**. Gorganizer saves Steam's changed files separately and restores the original game files. Then use Steam's **Properties → Installed Files → Verify integrity of game files**. When Steam finishes, choose **Verification Finished** in Gorganizer.

To inspect what was kept, choose **Show Saved Files…** in Steam Update Help. **Recover as New Mod…** copies the files you select into a new, disabled mod. If you select no files, it recovers them all. Enable that mod only if you need those files.

## Good to know

Do not edit files directly in the game's `Data` folder while mods are active. A direct edit can also change the mod's own copy of a file. Make mod changes through Gorganizer instead.

## Update or uninstall

From the folder you cloned, run `./gorganizer.sh update` to fetch updates from this checkout's configured branch, rebuild, and refresh the menu entry. It refuses if you have uncommitted changes, no configured update source, or local commits that are not in that source. It does not stop a running Gorganizer session. Close and reopen Gorganizer to use an installed update; `--restart` only prints a reminder to reopen it.

Close Gorganizer and your games before running `./gorganizer.sh uninstall`. It checks and restores your games before removing the menu entry and build files. By default, it keeps your mods, downloads, profiles, and settings. It also leaves SMAPI installed; use Steam's file verification if you want to remove it. To delete your Gorganizer data too, run `./gorganizer.sh uninstall --purge`. This needs an additional confirmation and cannot be undone. Uninstall refuses to proceed if a game is running or cannot be safely restored; it does not forcibly stop Gorganizer.

If uninstall warns that mods are still inside the cloned folder, follow the move command it prints **before deleting that folder**. Otherwise you will lose those mods.

## Troubleshooting

- Run `./gorganizerctl doctor` from the cloned folder to check your local setup, games, and background service. If the build tool is missing, run `./gorganizer.sh` first.
- Run `./gorganizerctl bug-report` to save a report on your computer. Nothing is uploaded. Review the bundle before you share it.
- After a crash, open Gorganizer again. It repairs interrupted changes on startup and asks you only when it cannot decide safely. For offline recovery, close Gorganizer and the game, then run `./gorganizerctl recover --game <id>` with a game ID such as `skyrimse`. If it asks for confirmation, inspect the files it names before following its instructions.
- If Nexus **Mod Manager Download** links do not open Gorganizer, run `./gorganizer.sh register` to refresh the menu entry and link handler.
- If a Windows modding tool needs .NET or Visual C++ inside its game's Proton setup, install **protontricks**. Gorganizer can use either a native or Flatpak installation.

## Stardew Valley and SMAPI

SMAPI is the mod loader used by Stardew Valley mods. Gorganizer supports the native Linux Steam build only. With Stardew Valley selected, use **Tools → SMAPI → Install SMAPI…** to download the official release and install it. Gorganizer checks its published SHA-256 checksum and runs the installer on a private copy of the game before applying the result. If a Steam update replaces SMAPI's launcher, choose **Tools → SMAPI → Repair SMAPI…** to reuse the saved installer. **Tools → SMAPI → Uninstall SMAPI…** removes the loader but keeps your mod folders.

SMAPI mod folders stay together when you install an archive. The **SMAPI** tab shows mod dependencies and can check smapi.io for updates. **Fetch Missing** downloads missing dependencies with Nexus Premium, or opens their Nexus pages so you can choose **Mod Manager Download**. New files created by mods while you play are kept in **Overwrite**.

## Tools

Under **Tools → External Tools...**, you can find installed tools or add your own. You can also choose **Install/Update LOOT…** for the official Windows portable LOOT release and **Sort with LOOT** to sort plugins. Gorganizer sorts using a separate working folder instead of giving LOOT your live mod files. Automatic LOOT sorting is not available for TTW. The built-in catalogue knows where to look for common modding tools, but does not download proprietary tools for you.

## For developers

```bash
make all       # generate Go protobuf files; build the daemon and maintenance tool
make gui       # build the Qt6 window
make test      # run Go tests with the race detector
make verify    # vet, race tests, and comment-policy check
make clean     # remove build files and generated Go protobuf files
```

For contribution rules and architecture notes, see [CLAUDE.md](CLAUDE.md). The daemon and maintenance tool live in `cmd/`; Go packages are in `internal/`; the gRPC definition is in `api/proto/`; the Qt6 GUI is in `src/`. Gorganizer is licensed under [GPL-3.0](LICENSE).
