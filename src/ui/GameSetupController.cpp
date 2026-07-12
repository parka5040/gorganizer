#include "GameSetupController.h"
#include "GrpcClient.h"
#include "SessionController.h"
#include "ModListWidget.h"
#include "GameDetector.h"
#include "TTWInstallDialog.h"
#include "Dialogs.h"

#include <QAction>
#include <QInputDialog>
#include <QMessageBox>
#include <QPushButton>
#include <QSet>
#include <QStatusBar>

#include <algorithm>

namespace gorganizer {

GameSetupController::GameSetupController(AppConfig& config, GrpcClient* grpc,
                                         SessionController* session,
                                         ModListWidget* modList, QAction* installTtwAction,
                                         QStatusBar* statusBar, QWidget* parentWindow)
    : QObject(parentWindow)
    , m_config(config)
    , m_grpc(grpc)
    , m_session(session)
    , m_modList(modList)
    , m_installTtwAction(installTtwAction)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    connect(m_grpc, &GrpcClient::recoveryPending, this, &GameSetupController::onRecoveryPending);
}

void GameSetupController::onActiveGameChanged(const GameInfo& game)
{
    if (m_installTtwAction) {
        const bool isTTW = (game.shortName == "ttw" && game.detected);
        bool ttwInstalled = false;
        if (isTTW && m_grpc->isConnected()) {
            QString verr;
            ttwInstalled = m_grpc->verifyTTWIntegrity(verr);
        }
        m_installTtwAction->setVisible(isTTW && !ttwInstalled);
    }
}

void GameSetupController::onAddNewGame()
{
    auto allDetected = GameDetector::detectAll();
    auto managed = m_config.managedGames();
    QSet<QString> managedSet(managed.begin(), managed.end());

    QStringList labels;
    std::vector<GameInfo> candidates;
    for (const auto& g : allDetected) {
        if (managedSet.contains(g.shortName))
            continue;
        candidates.push_back(g);
        if (g.appId == 0)
            labels.append(g.name);
        else
            labels.append(QString("%1 (App ID %2)").arg(g.name).arg(g.appId));
    }
    if (candidates.empty()) {
        dialogs::info(m_parentWindow, "Add New Game",
            "Every Bethesda game Steam can detect is already being managed.\n\n"
            "Install a new supported title in Steam, or use the manual-locate "
            "flow by editing ~/.config/gorganizer/gorganizer.conf.");
        return;
    }

    bool ok = false;
    QString chosenLabel = QInputDialog::getItem(m_parentWindow, "Add New Game",
        "Pick a detected game to start managing:", labels, 0, false, &ok);
    if (!ok) return;
    int idx = labels.indexOf(chosenLabel);
    if (idx < 0) return;
    const auto& chosen = candidates[idx];

    managed.push_back(chosen.shortName);
    m_config.setManagedGames(managed);

    if (m_grpc->isConnected())
        m_grpc->detectGames();
    else
        m_session->loadManagedGames();

    m_config.setActiveGameShortName(chosen.shortName);
    m_statusBar->showMessage(
        QString("%1 added. Use the Game dropdown to switch.").arg(chosen.name),
        5000);
}

void GameSetupController::onInstallTTW()
{
    if (!m_grpc->isConnected()) {
        dialogs::warn(m_parentWindow, "Daemon not connected",
            "The gorganizer daemon is not running. Start it before launching the TTW installer.");
        return;
    }
    TTWInstallDialog dlg(m_grpc, m_session->activeGame().shortName,
                         m_session->currentProfile(), m_parentWindow);
    dlg.exec();
    const bool installed =
        (dlg.outcome() == TTWInstallDialog::Accepted ||
         dlg.outcome() == TTWInstallDialog::InstalledOnly);

    if (installed) {
        auto managed = m_config.managedGames();
        if (std::find(managed.begin(), managed.end(), QString("ttw")) == managed.end()) {
            managed.push_back("ttw");
            m_config.setManagedGames(managed);
        }
        m_config.setActiveGameShortName("ttw");
    }

    if (m_grpc->isConnected())
        m_grpc->detectGames();
    if (!installed) {
        m_session->switchToGame(m_session->activeGame().appId);
        if (m_session->activeGame().detected && m_modList)
            m_modList->loadForGame(m_session->activeGame(), m_session->currentProfile());
    }
}

void GameSetupController::onRecoveryPending(const QString& gameId, const QString& dataPath,
                                            const QString& backupPath, const QString& reason)
{
    QMessageBox box(m_parentWindow);
    box.setWindowTitle("Recovery needed");
    box.setIcon(QMessageBox::Warning);
    box.setTextFormat(Qt::RichText);
    box.setText(
        QString("<b>Game: %1</b><br><br>%2<br><br>"
                "Data:&nbsp;<code>%3</code><br>"
                "Backup:&nbsp;<code>%4</code><br><br>"
                "Restoring will <b>delete the current Data/</b> and "
                "rename Data.orig/ back. Inspect the directories first "
                "if you're not sure where they came from.")
            .arg(gameId.toHtmlEscaped(),
                 reason.toHtmlEscaped(),
                 dataPath.toHtmlEscaped(),
                 backupPath.toHtmlEscaped()));
    auto* restoreBtn = box.addButton("Restore from Data.orig",
                                     QMessageBox::DestructiveRole);
    box.addButton("Cancel", QMessageBox::RejectRole);
    box.exec();
    if (box.clickedButton() == restoreBtn) {
        m_grpc->restoreFromBackup(gameId);
    }
}

}
