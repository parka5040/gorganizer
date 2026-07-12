#pragma once

#include "GameInfo.h"
#include <filesystem>

namespace gorganizer {

class DirectoryManager {
public:
    static bool createBaseDirectories(const std::filesystem::path& configDir,
                                      const std::filesystem::path& dataDir);

    static bool createGameDirectories(const GameInfo& game,
                                      const std::filesystem::path& dataRoot);
};

}
