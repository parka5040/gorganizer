#pragma once

#include <QString>
#include <QStringList>
#include <cstdint>
#include <filesystem>
#include <optional>
#include <vector>

namespace gorganizer {

enum class ModLoaderKind { None = 0, Smapi = 1 };

enum class GameInstallLayout { Unspecified = 0, DataRoot = 1, SmapiManifest = 2 };

struct GameCapabilities {
    bool plugins = true;
    bool ini = true;
    bool loot = true;
    ModLoaderKind modLoader = ModLoaderKind::None;
    GameInstallLayout installLayout = GameInstallLayout::DataRoot;
    bool manifestDependencies = false;
};

struct GameInfo {
    uint32_t appId = 0;
    QString name;
    QString shortName;
    std::filesystem::path installDir;
    std::filesystem::path dataDir;
    bool detected = false;

    bool synthetic = false;
    QString linkedFromShortName;
    bool vfsActive = false;

    QString dataSubpath;
    QString modsDirName;
    QStringList executablePaths;
    QStringList requiredDataFiles;
    QString seToolId;
    QString seDisplayName;
    QString seLoaderExe;
    QStringList canonicalMasters;
    QStringList canonicalDlcOrder;
    bool dataDirOptional = false;
    GameCapabilities capabilities;
    bool capabilitiesKnown = false;

    static const std::vector<GameInfo>& knownGames();
    static std::optional<GameInfo> findIn(const std::vector<GameInfo>& games, uint32_t appId);
    static std::optional<GameInfo> findByShortName(const QString& shortName);
    static std::optional<GameInfo> findByExeStem(const QString& stem);
    static QString modsDirPathFor(const QString& shortName);
    static QStringList mastersFor(const QString& shortName);
    static QStringList dlcOrderFor(const QString& shortName);
};

// Reports whether "Install Mod…" may use the local Data-root install dialog for this game.
bool usesLocalDataRootInstall(const GameInfo& game);

// Reports whether the game has a plugin load order, treating unknown capabilities as yes.
bool usesPlugins(const GameInfo& game);

// Reports whether the daemon manages a SMAPI mod loader for this detected game, treating unknown capabilities as no.
bool managesSmapi(const GameInfo& game);

}
