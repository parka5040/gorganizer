#pragma once

#include <QWidget>
#include <QToolButton>
#include <QComboBox>
#include "GameInfo.h"

namespace gorganizer {

// MO2-style compound run widget: a combo box next to a primary Run button.
//
// The combo lists every target known for the active game:
//   - "Launch <Game>"           → plain Steam launch, no script extender
//   - "Run <Tool> (installed)"  → exists on disk, use Proton+tool
//   - "Install <Tool>..."       → tool not yet on disk; click triggers a
//                                 Nexus download via the daemon
//
// The run button's label mirrors the current combo selection so the user
// can tell at a glance what will happen when they click ("Run xNVSE" vs
// "Run Fallout: New Vegas"). Selection persists per game via the parent's
// AppConfig.
class RunButtonWidget : public QWidget {
    Q_OBJECT
public:
    enum TargetType { TargetGame, TargetTool, TargetInstallTool };

    struct Target {
        TargetType type = TargetGame;
        QString label;     // shown in the combo
        QString toolId;    // "" for TargetGame, e.g. "xnvse" for a tool
    };

    explicit RunButtonWidget(QWidget* parent = nullptr);

    // Refreshes combo contents for the given game. Probes game.installDir
    // for known loader exes to decide whether each tool is "installed" or
    // needs to be fetched. `preferredToolId` selects a matching tool on
    // load if present; empty string → default to "Launch <Game>".
    void setGame(const GameInfo& game, const QString& preferredToolId = QString());

    // Returns the currently-selected target. Caller dispatches accordingly.
    Target currentTarget() const;

    // Back-compat: still reported as a boolean so the old launch path
    // keeps compiling; true whenever currentTarget().type == TargetTool.
    bool useToolEnabled() const;

signals:
    void runRequested();
    // Emitted when the combo changes so the host can persist the toolId.
    void targetChanged(const QString& toolId);

private:
    void rebuildCombo(const QString& preferredToolId);
    void syncRunLabel();

    GameInfo m_game;
    QComboBox* m_combo = nullptr;
    QToolButton* m_runBtn = nullptr;
};

} // namespace gorganizer
