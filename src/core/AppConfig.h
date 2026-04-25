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

    void setActiveGameAppId(uint32_t appId);
    uint32_t activeGameAppId() const;

    void setManagedGames(const std::vector<uint32_t>& appIds);
    std::vector<uint32_t> managedGames() const;

    void setPreferredStyle(const QString& style);
    QString preferredStyle() const;

    // Per-game "last active profile" so the UI reopens on the profile the user
    // actually left it on, not always "Default".
    void setLastProfileFor(const QString& gameShortName, const QString& profileName);
    QString lastProfileFor(const QString& gameShortName) const;

    // Per-game "last selected Run target". Empty string means "game itself"
    // (Steam launch); otherwise the tool ID (e.g. "xnvse") selected in the
    // Run combo. Persisted so the combo restores the user's choice next run.
    void setLastToolFor(const QString& gameShortName, const QString& toolId);
    QString lastToolFor(const QString& gameShortName) const;

    std::filesystem::path configDir() const;
    std::filesystem::path dataDir() const;

private:
    QSettings m_settings;
};

} // namespace gorganizer
