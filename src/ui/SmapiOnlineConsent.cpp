#include "SmapiOnlineConsent.h"
#include "AppConfig.h"
#include "NoticeBar.h"

namespace gorganizer {

SmapiOnlineConsent::SmapiOnlineConsent(AppConfig& config, NoticeBar* bar, QObject* parent)
    : QObject(parent)
    , m_config(config)
    , m_bar(bar)
    , m_preference(config.smapiOnlineChecks())
{
    connect(m_bar, &NoticeBar::dismissed, this, [this] { m_dismissed = true; });
}

bool SmapiOnlineConsent::allowed() const
{
    return m_config.smapiOnlineChecks() == std::optional<bool>(true);
}

void SmapiOnlineConsent::activeGameChanged(const GameInfo& game)
{
    m_game = game;
    if (!managesSmapi(game) || m_config.smapiOnlineChecks()) {
        m_bar->clear();
        return;
    }
    if (m_dismissed)
        return;
    m_bar->showNotice(NoticeBar::Kind::Info,
        QStringLiteral("Let Gorganizer check online for SMAPI and Stardew Valley mod updates? This contacts GitHub "
                       "for SMAPI releases and smapi.io, which receives your SMAPI mod list (mod IDs and versions) "
                       "and your SMAPI and game versions."),
        {{QStringLiteral("Check Online"), [this] { choose(true); }},
         {QStringLiteral("Don't Check"), [this] { choose(false); }}}, true);
}

void SmapiOnlineConsent::choose(bool on)
{
    if (!m_config.setSmapiOnlineChecks(on)) {
        m_dismissed = true;
        m_bar->showNotice(NoticeBar::Kind::Warning,
            QStringLiteral("Gorganizer could not save this setting. It will ask again next time."), {}, true);
        return;
    }
    m_preference = on;
    m_bar->clear();
    emit onlineChecksChanged(on);
}

void SmapiOnlineConsent::preferenceChanged()
{
    const auto preference = m_config.smapiOnlineChecks();
    const bool changed = preference != m_preference;
    m_preference = preference;
    activeGameChanged(m_game);
    if (changed)
        emit onlineChecksChanged(preference.value_or(false));
}

}
