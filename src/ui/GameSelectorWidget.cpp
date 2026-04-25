#include "GameSelectorWidget.h"

namespace gorganizer {

GameSelectorWidget::GameSelectorWidget(QWidget* parent)
    : QComboBox(parent)
{
    connect(this, &QComboBox::currentIndexChanged, this, [this](int index) {
        if (index >= 0 && index < static_cast<int>(m_games.size()))
            emit gameChanged(m_games[index].appId);
    });
}

void GameSelectorWidget::setGames(const std::vector<GameInfo>& games)
{
    m_games = games;
    blockSignals(true);
    clear();
    for (const auto& game : m_games)
        addItem(game.name, game.appId);
    blockSignals(false);
}

void GameSelectorWidget::setActiveGame(uint32_t appId)
{
    for (int i = 0; i < static_cast<int>(m_games.size()); ++i) {
        if (m_games[i].appId == appId) {
            setCurrentIndex(i);
            return;
        }
    }
    // Fallback to first game if requested ID not found
    if (!m_games.empty())
        setCurrentIndex(0);
}

GameInfo GameSelectorWidget::currentGame() const
{
    int idx = currentIndex();
    if (idx >= 0 && idx < static_cast<int>(m_games.size()))
        return m_games[idx];
    return {};
}

} // namespace gorganizer
