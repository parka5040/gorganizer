#pragma once

#include <QString>
#include <QStringList>
#include <cstdint>
#include <filesystem>
#include <optional>
#include <vector>

namespace gorganizer {

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

    static const std::vector<GameInfo>& knownGames();
    static std::optional<GameInfo> findIn(const std::vector<GameInfo>& games, uint32_t appId);
    static std::optional<GameInfo> findByShortName(const QString& shortName);
    static std::optional<GameInfo> findByExeStem(const QString& stem);
    static QString modsDirPathFor(const QString& shortName);
    static QStringList mastersFor(const QString& shortName);
    static QStringList dlcOrderFor(const QString& shortName);
};

}
