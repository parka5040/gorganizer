#pragma once

#include <QString>
#include <filesystem>
#include <vector>

namespace gorganizer {

struct PluginEntry {
    QString filename;
    enum Type { ESM, ESL, ESP } type;
};

class PluginScanner {
public:
    static std::vector<PluginEntry> scan(const std::filesystem::path& dataDir);
};

}
