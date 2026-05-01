#pragma once

#include <QWidget>
#include <QToolButton>
#include <QComboBox>
#include "GameInfo.h"

namespace gorganizer {

// MO2-style compound run widget: combo box selector + primary Run button.
class RunButtonWidget : public QWidget {
    Q_OBJECT
public:
    enum TargetType { TargetGame, TargetTool, TargetInstallTool };

    struct Target {
        TargetType type = TargetGame;
        QString label;
        QString toolId;
    };

    explicit RunButtonWidget(QWidget* parent = nullptr);

    void setGame(const GameInfo& game, const QString& preferredToolId = QString());

    // Disables the xNVSE row when FalloutNV.exe is FNV4GB-patched (loader bypasses patcher entry point).
    void setFourGBPatched(bool patched);

    // Disables FNV's Steam-launch row while TTW's VFS is active (mutex group conflict).
    void setTTWVfsActive(bool active);

    Target currentTarget() const;

    bool useToolEnabled() const;

signals:
    void runRequested();
    void targetChanged(const QString& toolId);

private:
    void rebuildCombo(const QString& preferredToolId);
    void syncRunLabel();

    GameInfo m_game;
    QComboBox* m_combo = nullptr;
    QToolButton* m_runBtn = nullptr;
    bool m_fourGBPatched = false;
    bool m_ttwVfsActive = false;
    QString m_lastPreferredToolId;
};

} // namespace gorganizer
