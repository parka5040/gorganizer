#pragma once

#include <QObject>
#include <optional>
#include "GameInfo.h"

namespace gorganizer {

class AppConfig;
class NoticeBar;

class SmapiOnlineConsent : public QObject {
    Q_OBJECT
public:
    // Connects the saved SMAPI online-check preference to its notice bar.
    SmapiOnlineConsent(AppConfig& config, NoticeBar* bar, QObject* parent = nullptr);
    // Reports whether automatic SMAPI online checks have been allowed.
    bool allowed() const;
    // Shows the consent question for an active SMAPI game with no saved preference.
    void activeGameChanged(const GameInfo& game);
    // Reconciles the notice and broadcasts changes made in Settings.
    void preferenceChanged();

signals:
    void onlineChecksChanged(bool allowed);

private:
    // Saves the chosen preference and broadcasts it, or shows a save warning.
    void choose(bool on);

    AppConfig& m_config;
    NoticeBar* m_bar;
    GameInfo m_game;
    std::optional<bool> m_preference;
    bool m_dismissed = false;
};

}
