#include "AppConfig.h"
#include "GameInfo.h"
#include "Paths.h"
#include <QStringList>

namespace gorganizer {

AppConfig::AppConfig()
    : m_settings(
          QString::fromStdString((Paths::appConfigDir() / "gorganizer.conf").string()),
          QSettings::IniFormat)
{
}

bool AppConfig::isFirstBoot() const
{
    return !m_settings.value("setup/complete", false).toBool();
}

void AppConfig::markSetupComplete()
{
    m_settings.setValue("setup/complete", true);
    m_settings.sync();
}

void AppConfig::setActiveGameShortName(const QString& shortName)
{
    m_settings.setValue("game/activeShortName", shortName);
    if (auto info = GameInfo::findByShortName(shortName))
        m_settings.setValue("game/activeAppId", info->appId);
    else
        m_settings.setValue("game/activeAppId", 0);
    m_settings.sync();
}

QString AppConfig::activeGameShortName() const
{
    QString sn = m_settings.value("game/activeShortName").toString();
    if (!sn.isEmpty())
        return sn;
    uint32_t appId = m_settings.value("game/activeAppId", 0).toUInt();
    if (appId == 0)
        return {};
    if (auto info = GameInfo::findIn(GameInfo::knownGames(), appId))
        return info->shortName;
    return {};
}

void AppConfig::setActiveGameAppId(uint32_t appId)
{
    if (auto info = GameInfo::findIn(GameInfo::knownGames(), appId))
        setActiveGameShortName(info->shortName);
    else
        m_settings.setValue("game/activeAppId", appId);
    m_settings.sync();
}

uint32_t AppConfig::activeGameAppId() const
{
    if (auto info = GameInfo::findByShortName(activeGameShortName()))
        return info->appId;
    return m_settings.value("game/activeAppId", 0).toUInt();
}

void AppConfig::setManagedGames(const std::vector<QString>& shortNames)
{
    QStringList list;
    for (const auto& sn : shortNames)
        list.append(sn);
    m_settings.setValue("game/managedGamesV2", list.join(','));

    QStringList legacy;
    for (const auto& sn : shortNames) {
        if (auto info = GameInfo::findByShortName(sn))
            if (info->appId != 0)
                legacy.append(QString::number(info->appId));
    }
    m_settings.setValue("game/managedGames", legacy.join(','));
    m_settings.sync();
}

std::vector<QString> AppConfig::managedGames() const
{
    std::vector<QString> shortNames;
    QString raw = m_settings.value("game/managedGamesV2").toString();
    if (!raw.isEmpty()) {
        for (const auto& s : raw.split(',', Qt::SkipEmptyParts))
            shortNames.push_back(s);
        return shortNames;
    }
    QString legacy = m_settings.value("game/managedGames").toString();
    if (legacy.isEmpty())
        return shortNames;
    for (const auto& s : legacy.split(',', Qt::SkipEmptyParts)) {
        uint32_t appId = s.toUInt();
        if (appId == 0)
            continue;
        if (auto info = GameInfo::findIn(GameInfo::knownGames(), appId))
            shortNames.push_back(info->shortName);
    }
    return shortNames;
}

void AppConfig::setManagedGamesByAppId(const std::vector<uint32_t>& appIds)
{
    std::vector<QString> shortNames;
    for (uint32_t id : appIds) {
        if (auto info = GameInfo::findIn(GameInfo::knownGames(), id))
            shortNames.push_back(info->shortName);
    }
    setManagedGames(shortNames);
}

std::vector<uint32_t> AppConfig::managedGamesByAppId() const
{
    std::vector<uint32_t> ids;
    for (const auto& sn : managedGames()) {
        if (auto info = GameInfo::findByShortName(sn))
            if (info->appId != 0)
                ids.push_back(info->appId);
    }
    return ids;
}

void AppConfig::setPreferredStyle(const QString& style)
{
    m_settings.setValue("ui/style", style);
    m_settings.sync();
}

QString AppConfig::preferredStyle() const
{
    const QString s = m_settings.value("ui/style").toString();
    return s.isEmpty() ? QStringLiteral("Neutral") : s;
}

void AppConfig::setAppearanceMode(const QString& mode)
{
    m_settings.setValue("ui/appearanceMode", mode);
    m_settings.sync();
}

QString AppConfig::appearanceMode() const
{
    QString m = m_settings.value("ui/appearanceMode").toString();
    if (m != "system" && m != "light" && m != "dark")
        return "system";
    return m;
}

void AppConfig::setCollapsedSeparatorView(bool on)
{
    m_settings.setValue("ui/collapsedSeparatorView", on);
    m_settings.sync();
}

bool AppConfig::collapsedSeparatorView() const
{
    return m_settings.value("ui/collapsedSeparatorView", false).toBool();
}

void AppConfig::setLastProfileFor(const QString& gameShortName, const QString& profileName)
{
    if (gameShortName.isEmpty() || profileName.isEmpty())
        return;
    m_settings.setValue(QString("profiles/lastActive/%1").arg(gameShortName), profileName);
    m_settings.sync();
}

QString AppConfig::lastProfileFor(const QString& gameShortName) const
{
    if (gameShortName.isEmpty())
        return {};
    return m_settings.value(QString("profiles/lastActive/%1").arg(gameShortName)).toString();
}

void AppConfig::setLastToolFor(const QString& gameShortName, const QString& toolId)
{
    if (gameShortName.isEmpty())
        return;
    m_settings.setValue(QString("tools/lastActive/%1").arg(gameShortName), toolId);
    m_settings.sync();
}

QString AppConfig::lastToolFor(const QString& gameShortName) const
{
    if (gameShortName.isEmpty())
        return {};
    return m_settings.value(QString("tools/lastActive/%1").arg(gameShortName)).toString();
}

std::filesystem::path AppConfig::configDir() const
{
    return Paths::appConfigDir();
}

std::filesystem::path AppConfig::dataDir() const
{
    return Paths::appDataDir();
}

}
