#include "DirectoryManager.h"

namespace gorganizer {

bool DirectoryManager::createBaseDirectories(const std::filesystem::path& configDir,
                                             const std::filesystem::path& dataDir)
{
    std::error_code ec;
    std::filesystem::create_directories(configDir, ec);
    if (ec) return false;
    std::filesystem::create_directories(dataDir, ec);
    return !ec;
}

bool DirectoryManager::createGameDirectories(const GameInfo& game,
                                             const std::filesystem::path& dataRoot)
{
    std::error_code ec;
    auto gameDir = dataRoot / game.shortName.toStdString();
    std::filesystem::create_directories(gameDir / "mods", ec);
    if (ec) return false;
    std::filesystem::create_directories(gameDir / "profiles" / "Default", ec);
    if (ec) return false;
    std::filesystem::create_directories(gameDir / "overwrite", ec);
    return !ec;
}

} // namespace gorganizer
