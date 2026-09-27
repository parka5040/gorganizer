#include "GameSetupController.h"
#include "GrpcClient.h"
#include "SessionController.h"
#include "ModListWidget.h"
#include "GameDetector.h"
#include "TTWInstallDialog.h"
#include "Dialogs.h"
#include "InstallErrorText.h"

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
    connect(m_grpc, &GrpcClient::vfsStatusReceived, this, &GameSetupController::onVfsStatusReceived);
    connect(m_grpc, &GrpcClient::vfsStatusChanged, this, &GameSetupController::onVfsStatusReceived);
    connect(m_grpc, &GrpcClient::vfsStatusQueried, this, &GameSetupController::onVfsStatusQueried);
    connect(m_grpc, &GrpcClient::vfsStatusQueryFailed, this, &GameSetupController::onVfsStatusQueryFailed);
    connect(m_grpc, &GrpcClient::rpcError, this, &GameSetupController::onRpcError);
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

void GameSetupController::onRecoveryPending(const GrpcRecoveryPending& recovery)
{
    m_latestRecoveries.insert(recovery.gameId, recovery);
    m_lastRecoveryEventSeq.insert(recovery.gameId, ++m_recoveryEventSeq);
    if (m_shownRecoveryIds.contains(recovery.recoveryId))
        return;
    m_seenRecoveryIds[recovery.gameId].insert(recovery.recoveryId);
    m_shownRecoveryIds.insert(recovery.recoveryId);
    queueRecovery(recovery);
}

void GameSetupController::onVfsStatusReceived(const GrpcVFSStatus& status)
{
    if (!status.hasPendingRecovery)
        return;
    const GrpcRecoveryPending& recovery = status.pendingRecovery;
    m_latestRecoveries.insert(status.gameId, recovery);
    if (status.gameId != m_session->activeGame().shortName
        || m_pendingReviewQueries.contains(status.gameId)
        || m_shownRecoveryIds.contains(recovery.recoveryId))
        return;
    m_seenRecoveryIds[status.gameId].insert(recovery.recoveryId);
    m_shownRecoveryIds.insert(recovery.recoveryId);
    queueRecovery(recovery);
}

void GameSetupController::onVfsStatusQueried(quint64 requestId, const GrpcVFSStatus& status)
{
    if (m_pendingReviewQueries.value(status.gameId) != requestId)
        return;
    m_pendingReviewQueries.remove(status.gameId);
    if (!status.hasPendingRecovery) {
        m_statusBar->showMessage("No recovery is waiting for this game.", 5000);
        return;
    }
    const GrpcRecoveryPending& recovery = status.pendingRecovery;
    m_latestRecoveries.insert(status.gameId, recovery);
    m_seenRecoveryIds[status.gameId].insert(recovery.recoveryId);
    m_shownRecoveryIds.insert(recovery.recoveryId);
    queueRecovery(recovery);
}

void GameSetupController::onVfsStatusQueryFailed(quint64 requestId, const QString& gameId, const QString&)
{
    if (m_pendingReviewQueries.value(gameId) != requestId)
        return;
    m_pendingReviewQueries.remove(gameId);
    m_statusBar->showMessage("Gorganizer couldn't check the recovery details. Try Review… again.", 5000);
}

void GameSetupController::reviewRecovery(const QString& gameId)
{
    if (!m_latestRecoveries.contains(gameId)) {
        if (!m_pendingReviewQueries.contains(gameId)) {
            m_pendingReviewQueries.insert(gameId, m_grpc->queryVfsStatus(gameId));
            m_statusBar->showMessage("Waiting for the recovery details from Gorganizer…", 5000);
        }
        return;
    }
    const GrpcRecoveryPending recovery = m_latestRecoveries.value(gameId);
    m_seenRecoveryIds[gameId].insert(recovery.recoveryId);
    m_shownRecoveryIds.insert(recovery.recoveryId);
    queueRecovery(recovery);
}

void GameSetupController::queueRecovery(const GrpcRecoveryPending& recovery)
{
    if (m_showingRecovery && m_showingRecoveryGameId == recovery.gameId
        && m_showingRecoveryId == recovery.recoveryId)
        return;
    m_queuedRecoveries.insert(recovery.gameId, recovery);
    if (m_showingRecovery)
        return;
    m_showingRecovery = true;
    while (!m_queuedRecoveries.isEmpty()) {
        const GrpcRecoveryPending next = m_queuedRecoveries.take(m_queuedRecoveries.cbegin().key());
        m_showingRecoveryGameId = next.gameId;
        m_showingRecoveryId = next.recoveryId;

        QString gameName = next.gameId;
        if (const auto known = GameInfo::findByShortName(next.gameId); known && !known->name.isEmpty())
            gameName = known->name;
        if (m_session->activeGame().shortName == next.gameId && !m_session->activeGame().name.isEmpty())
            gameName = m_session->activeGame().name;

        QMessageBox box(m_parentWindow);
        box.setWindowTitle("Recovery needed");
        box.setIcon(QMessageBox::Warning);
        box.setTextFormat(Qt::PlainText);
        box.setDetailedText(QString("Reason: %1\nGame folder: %2\nBackup folder: %3")
                                .arg(next.reason, next.dataPath, next.backupPath));
        QPushButton* action = nullptr;
        switch (next.kind) {
        case GrpcRecoveryKind::Data:
            box.setText(QString("Restore the original game files for %1? This replaces the current "
                                "managed game folder with its backup. Files not captured in the backup may be lost.")
                            .arg(gameName));
            action = box.addButton("Restore original files", QMessageBox::DestructiveRole);
            break;
        case GrpcRecoveryKind::ModLoader:
            box.setText(QString("An interrupted SMAPI change for %1 needs recovery.").arg(gameName));
            action = box.addButton("Retry SMAPI recovery", QMessageBox::ActionRole);
            break;
        case GrpcRecoveryKind::GameRoot:
            box.setText(QString("Files added beside the %1 program need recovery.").arg(gameName));
            action = box.addButton("Retry game-file recovery", QMessageBox::ActionRole);
            break;
        default:
            box.setText(QString("Gorganizer found a recovery issue for %1 it cannot explain safely. "
                                "Update Gorganizer before continuing.").arg(gameName));
            box.addButton("Close", QMessageBox::RejectRole);
            break;
        }
        if (action)
            box.addButton("Cancel", QMessageBox::RejectRole);
        box.exec();
        if (action && box.clickedButton() == action) {
            m_lastRestoreAttemptSeq.insert(next.gameId, m_recoveryEventSeq);
            m_grpc->restoreFromBackup(next.gameId, next.kind, next.recoveryId);
        }
        m_showingRecoveryGameId.clear();
        m_showingRecoveryId.clear();
    }
    m_showingRecovery = false;
}

void GameSetupController::onRpcError(const QString& method, const QString& error)
{
    if (method != QLatin1String("RestoreFromBackup"))
        return;
    const InstallError parsed = parseInstallError(error);
    if (parsed.token != QLatin1String("recovery_stale"))
        return;
    const QString gameId = parsed.fields.value(QStringLiteral("game"));
    const bool reannounced = m_latestRecoveries.contains(gameId)
        && m_lastRecoveryEventSeq.value(gameId) > m_lastRestoreAttemptSeq.value(gameId);
    for (const QString& id : m_seenRecoveryIds.value(gameId))
        m_shownRecoveryIds.remove(id);
    m_seenRecoveryIds.remove(gameId);
    dialogs::plainInfo(m_parentWindow, "Recovery changed", daemonErrorMessage(error));
    if (reannounced && m_latestRecoveries.contains(gameId)
        && !m_seenRecoveryIds.value(gameId).contains(m_latestRecoveries.value(gameId).recoveryId))
        reviewRecovery(gameId);
}

}
