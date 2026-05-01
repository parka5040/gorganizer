#pragma once

#include <QComboBox>
#include "GameInfo.h"
#include <vector>

namespace gorganizer {

class GameSelectorWidget : public QComboBox {
    Q_OBJECT
public:
    explicit GameSelectorWidget(QWidget* parent = nullptr);

    void setGames(const std::vector<GameInfo>& games);
    void setActiveGame(uint32_t appId);
    // Prefer shortName-keyed selection since synthetic games share appId=0.
    void setActiveGameByShortName(const QString& shortName);
    GameInfo currentGame() const;

signals:
    void gameChanged(uint32_t appId);
    void gameChangedByShortName(const QString& shortName);

private:
    std::vector<GameInfo> m_games;
};

} // namespace gorganizer
