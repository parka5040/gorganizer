#include "PluginScanner.h"
#include <algorithm>

namespace gorganizer {

std::vector<PluginEntry> PluginScanner::scan(const std::filesystem::path& dataDir)
{
    std::vector<PluginEntry> plugins;
    if (dataDir.empty() || !std::filesystem::exists(dataDir))
        return plugins;

    std::error_code ec;
    for (const auto& entry : std::filesystem::directory_iterator(dataDir, ec)) {
        if (!entry.is_regular_file())
            continue;

        auto ext = entry.path().extension().string();
        std::transform(ext.begin(), ext.end(), ext.begin(), ::tolower);

        PluginEntry::Type type;
        if (ext == ".esm")
            type = PluginEntry::ESM;
        else if (ext == ".esl")
            type = PluginEntry::ESL;
        else if (ext == ".esp")
            type = PluginEntry::ESP;
        else
            continue;

        plugins.push_back({
            QString::fromStdString(entry.path().filename().string()),
            type
        });
    }

    std::sort(plugins.begin(), plugins.end(),
              [](const PluginEntry& a, const PluginEntry& b) {
                  if (a.type != b.type)
                      return a.type < b.type;
                  return a.filename.compare(b.filename, Qt::CaseInsensitive) < 0;
              });

    return plugins;
}

} // namespace gorganizer
