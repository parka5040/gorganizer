#include "Paths.h"
#include <QDir>
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

std::filesystem::path steamRoot()
{
    // Primary location
    auto primary = dataHome() / "Steam";
    if (std::filesystem::exists(primary / "steamapps"))
        return primary;

    // Symlink fallback
    auto home = std::filesystem::path(QDir::homePath().toStdString());
    auto symlink = home / ".steam" / "steam";
    if (std::filesystem::exists(symlink / "steamapps"))
        return std::filesystem::canonical(symlink);

    // Flatpak fallback
    auto flatpak = home / ".var" / "app" / "com.valvesoftware.Steam" / ".local" / "share" / "Steam";
    if (std::filesystem::exists(flatpak / "steamapps"))
        return flatpak;

    return {};
}

} // namespace gorganizer::Paths
