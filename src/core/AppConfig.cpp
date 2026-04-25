#include "AppConfig.h"
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

void AppConfig::setActiveGameAppId(uint32_t appId)
{
    m_settings.setValue("game/activeAppId", appId);
    m_settings.sync();
}

uint32_t AppConfig::activeGameAppId() const
{
    return m_settings.value("game/activeAppId", 0).toUInt();
}

void AppConfig::setManagedGames(const std::vector<uint32_t>& appIds)
{
    QStringList list;
    for (uint32_t id : appIds)
        list.append(QString::number(id));
    m_settings.setValue("game/managedGames", list.join(','));
    m_settings.sync();
}

std::vector<uint32_t> AppConfig::managedGames() const
{
    std::vector<uint32_t> ids;
    QString raw = m_settings.value("game/managedGames").toString();
    if (raw.isEmpty())
        return ids;
    for (const auto& s : raw.split(',', Qt::SkipEmptyParts))
        ids.push_back(s.toUInt());
    return ids;
}

void AppConfig::setPreferredStyle(const QString& style)
{
    m_settings.setValue("ui/style", style);
    m_settings.sync();
}

QString AppConfig::preferredStyle() const
{
    return m_settings.value("ui/style").toString();
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

} // namespace gorganizer
