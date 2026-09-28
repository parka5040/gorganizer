#include "Paths.h"
#include <QDir>
#include <algorithm>
#include <cstdlib>

namespace gorganizer::Paths {

std::filesystem::path configHome()
{
    if (const char* xdg = std::getenv("XDG_CONFIG_HOME"); xdg && xdg[0])
        return xdg;
    return std::filesystem::path(QDir::homePath().toStdString()) / ".config";
}

std::filesystem::path dataHome()
{
    if (const char* xdg = std::getenv("XDG_DATA_HOME"); xdg && xdg[0])
        return xdg;
    return std::filesystem::path(QDir::homePath().toStdString()) / ".local" / "share";
}

std::filesystem::path appConfigDir()
{
    return configHome() / "gorganizer";
}

std::filesystem::path appDataDir()
{
    return dataHome() / "gorganizer";
}

std::vector<std::filesystem::path> steamRoots()
{
    const auto home = std::filesystem::path(QDir::homePath().toStdString());
    std::vector<std::filesystem::path> candidates{
        home / ".local" / "share" / "Steam",
        home / ".steam" / "steam",
        home / ".steam" / "root",
        home / ".var" / "app" / "com.valvesoftware.Steam" / ".local" / "share" / "Steam",
        home / "snap" / "steam" / "common" / ".local" / "share" / "Steam"
    };
    if (const char* xdg = std::getenv("XDG_DATA_HOME"); xdg && xdg[0])
        candidates.push_back(std::filesystem::path(xdg) / "Steam");

    std::vector<std::filesystem::path> roots;
    for (const auto& candidate : candidates) {
        std::error_code ec;
        if (!std::filesystem::is_directory(candidate / "steamapps", ec))
            continue;
        auto resolved = std::filesystem::canonical(candidate, ec);
        if (!ec && std::find(roots.begin(), roots.end(), resolved) == roots.end())
            roots.push_back(resolved);
    }
    return roots;
}

std::filesystem::path steamRoot()
{
    auto roots = steamRoots();
    return roots.empty() ? std::filesystem::path{} : roots.front();
}

}
