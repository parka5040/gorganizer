#pragma once

#include <QSettings>
#include <QString>
#include <cstdint>
#include <filesystem>
#include <vector>

namespace gorganizer {

class AppConfig {
public:
    AppConfig();

    bool isFirstBoot() const;
    void markSetupComplete();

    void setActiveGameShortName(const QString& shortName);
    QString activeGameShortName() const;

    // Deprecated appId-keyed shims kept one release for legacy daemons.
    void setActiveGameAppId(uint32_t appId);
    uint32_t activeGameAppId() const;

    void setManagedGames(const std::vector<QString>& shortNames);
    std::vector<QString> managedGames() const;

    void setManagedGamesByAppId(const std::vector<uint32_t>& appIds);
    std::vector<uint32_t> managedGamesByAppId() const;

    void setPreferredStyle(const QString& style);
    QString preferredStyle() const;

    // Light/Dark/System appearance mode; "system" follows the OS color scheme.
    void setAppearanceMode(const QString& mode);
    QString appearanceMode() const;

    void setCollapsedSeparatorView(bool on);
    bool collapsedSeparatorView() const;

    void setLastProfileFor(const QString& gameShortName, const QString& profileName);
    QString lastProfileFor(const QString& gameShortName) const;

    // Per-game last selected Run target (empty = launch game itself).
    void setLastToolFor(const QString& gameShortName, const QString& toolId);
    QString lastToolFor(const QString& gameShortName) const;

    std::filesystem::path configDir() const;
    std::filesystem::path dataDir() const;

private:
    QSettings m_settings;
};

}
