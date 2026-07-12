#pragma once

#include "GameInfo.h"
#include <filesystem>
#include <optional>
#include <vector>

namespace gorganizer {

class GameDetector {
public:
    static std::optional<std::filesystem::path> findSteamRoot();
    static std::vector<std::filesystem::path> findLibraryFolders(const std::filesystem::path& steamRoot);
    static std::vector<GameInfo> detectGames(const std::vector<std::filesystem::path>& libraryFolders);
    static std::vector<GameInfo> detectAll();

    // Build a GameInfo from a user-selected executable for Lutris/GOG/manual installs.
    static std::optional<GameInfo> fromExecutable(const std::filesystem::path& exePath);

private:
    static std::optional<GameInfo> parseAppManifest(
        const std::filesystem::path& acfPath,
        const std::filesystem::path& libraryFolder);
};

}
