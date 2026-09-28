#!/bin/bash

# Reports deployed farms and managed SMAPI installations from the saved games.
deployed_games_report() {
    local config_path="${XDG_CONFIG_HOME:-$HOME/.config}/gorganizer/config.json"
    if [ ! -e "$config_path" ]; then
        return 0
    fi
    if ! command -v python3 >/dev/null 2>&1 || [ ! -f "$config_path" ]; then
        return 2
    fi
    python3 - "$config_path" <<'PY'
import json
import os
import sys

try:
    with open(sys.argv[1], encoding="utf-8") as source:
        config = json.load(source)
    games = config["games"]
    if not isinstance(games, dict):
        raise ValueError("Invalid games")
    deployed = []
    modloaders = []
    for game_id, game in games.items():
        if not isinstance(game, dict):
            raise ValueError("Invalid game")
        install = game.get("install_path")
        if not install:
            continue
        subpath = game.get("data_subpath") or "Data"
        if not isinstance(install, str) or not os.path.isabs(install) or not isinstance(subpath, str):
            raise ValueError("Invalid game path")
        install = os.path.normpath(install)
        data = os.path.normpath(os.path.join(install, subpath))
        name = game.get("name") or game_id
        if not isinstance(name, str):
            raise ValueError("Invalid game name")
        markers = (
            data + ".orig",
            os.path.join(data, ".gorganizer-overlay.json"),
            data + ".gorganizer-activating",
            data + ".gorganizer-restoring",
            data + ".gorganizer-session",
            os.path.join(install, ".gorganizer-root-manifest.json"),
            os.path.join(install, ".gorganizer-root-intent.json"),
            os.path.join(install, ".gorganizer-modloader-intent.json"),
        )
        if any(os.path.lexists(marker) for marker in markers):
            deployed.append(name)
        if os.path.lexists(os.path.join(install, ".gorganizer-modloader.json")):
            modloaders.append(name)
except (OSError, ValueError, TypeError, KeyError):
    sys.exit(2)

if deployed:
    print("deployed: " + ", ".join(deployed))
if modloaders:
    print("modloader: " + ", ".join(modloaders))
sys.exit(1 if deployed else 0)
PY
}

# Refuses removal if saved games have changes applied or cannot be checked.
verify_games_restored() {
    local report status=0 deployed="" modloaders="" line
    report="$(deployed_games_report)" || status=$?
    if [ "$status" -ne 0 ] && [ "$status" -ne 1 ]; then
        printf '%s\n' 'Gorganizer cannot verify that your games are restored, so nothing was removed.' >&2
        return 1
    fi
    while IFS= read -r line; do
        case "$line" in
            'deployed: '*) deployed="${line#deployed: }" ;;
            'modloader: '*) modloaders="${line#modloader: }" ;;
        esac
    done <<< "$report"
    if [ "$status" -ne 0 ]; then
        printf '%s\n' "These games still have Gorganizer changes applied: $deployed. Open Gorganizer, choose \"Unmount Mods\" for each game (and uninstall SMAPI from Tools → SMAPI for Stardew Valley), close Gorganizer, then try again. Nothing was removed." >&2
        return 1
    fi
    if [ -n "$modloaders" ]; then
        printf '%s\n' "SMAPI is still installed by Gorganizer for: $modloaders. To remove it, open Gorganizer and use Tools → SMAPI." >&2
    fi
}
