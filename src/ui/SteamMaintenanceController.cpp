#include "SteamMaintenanceController.h"
#include "Dialogs.h"
#include "ErrorPresenter.h"
#include "GrpcClient.h"
#include "SessionController.h"

#include <QAction>
#include <QDate>
#include <QDesktopServices>
#include <QDialog>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QHBoxLayout>
#include <QInputDialog>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLabel>
#include <QListWidget>
#include <QLocale>
#include <QPushButton>
#include <QStatusBar>
#include <QUrl>
#include <QVBoxLayout>

namespace gorganizer {

SteamMaintenanceController::SteamMaintenanceController(GrpcClient* grpc, SessionController* session,
                                                       QAction* helpAction, QAction* pauseAction,
                                                       QStatusBar* statusBar, QWidget* parentWindow)
    : QObject(parentWindow)
    , m_grpc(grpc)
    , m_session(session)
    , m_helpAction(helpAction)
    , m_pauseAction(pauseAction)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    m_panel = new QDialog(parentWindow);
    m_panel->setWindowTitle("Steam Update Help");
    m_panel->setModal(false);
    auto* panelLayout = new QVBoxLayout(m_panel);
    m_explanation = new QLabel(m_panel);
    m_explanation->setTextFormat(Qt::PlainText);
    m_explanation->setWordWrap(true);
    m_explanation->setMinimumWidth(440);
    panelLayout->addWidget(m_explanation);
    auto* panelButtons = new QHBoxLayout;
    m_pauseButton = new QPushButton("Pause Mods", m_panel);
    m_finishButton = new QPushButton(m_panel);
    m_showFilesButton = new QPushButton("Show Saved Files…", m_panel);
    auto* closePanel = new QPushButton("Close", m_panel);
    panelButtons->addWidget(m_pauseButton);
    panelButtons->addWidget(m_finishButton);
    panelButtons->addWidget(m_showFilesButton);
    panelButtons->addStretch();
    panelButtons->addWidget(closePanel);
    panelLayout->addLayout(panelButtons);
    connect(m_pauseButton, &QPushButton::clicked, this, &SteamMaintenanceController::pauseMods);
    connect(m_finishButton, &QPushButton::clicked, this, &SteamMaintenanceController::finishSteam);
    connect(m_showFilesButton, &QPushButton::clicked, this, &SteamMaintenanceController::showSavedFiles);
    connect(closePanel, &QPushButton::clicked, m_panel, &QDialog::hide);

    m_savedDialog = new QDialog(parentWindow);
    m_savedDialog->setWindowTitle("Saved Steam Files");
    m_savedDialog->setModal(false);
    auto* savedLayout = new QVBoxLayout(m_savedDialog);
    auto* batchLabel = new QLabel("Saved sets (date and number of files):", m_savedDialog);
    savedLayout->addWidget(batchLabel);
    m_batches = new QListWidget(m_savedDialog);
    savedLayout->addWidget(m_batches);
    savedLayout->addWidget(new QLabel("Files in the selected set (select files to recover, or leave none selected to recover all):", m_savedDialog));
    m_files = new QListWidget(m_savedDialog);
    m_files->setSelectionMode(QAbstractItemView::ExtendedSelection);
    savedLayout->addWidget(m_files);
    m_fileMessage = new QLabel(m_savedDialog);
    m_fileMessage->setTextFormat(Qt::PlainText);
    m_fileMessage->setWordWrap(true);
    savedLayout->addWidget(m_fileMessage);
    auto* savedButtons = new QHBoxLayout;
    m_openButton = new QPushButton("Open Folder", m_savedDialog);
    m_recoverButton = new QPushButton("Recover as New Mod…", m_savedDialog);
    m_deleteButton = new QPushButton("Delete Saved Files…", m_savedDialog);
    auto* closeSaved = new QPushButton("Close", m_savedDialog);
    savedButtons->addWidget(m_openButton);
    savedButtons->addWidget(m_recoverButton);
    savedButtons->addWidget(m_deleteButton);
    savedButtons->addStretch();
    savedButtons->addWidget(closeSaved);
    savedLayout->addLayout(savedButtons);
    m_savedDialog->resize(650, 420);
    connect(m_batches, &QListWidget::currentRowChanged, this, &SteamMaintenanceController::showBatchFiles);
    connect(m_openButton, &QPushButton::clicked, this, &SteamMaintenanceController::openBatchFolder);
    connect(m_recoverButton, &QPushButton::clicked, this, &SteamMaintenanceController::recoverFiles);
    connect(m_deleteButton, &QPushButton::clicked, this, &SteamMaintenanceController::deleteBatch);
    connect(closeSaved, &QPushButton::clicked, m_savedDialog, &QDialog::hide);

    connect(m_helpAction, &QAction::triggered, this, &SteamMaintenanceController::showHelp);
    connect(m_pauseAction, &QAction::triggered, this, &SteamMaintenanceController::pauseMods);
    connect(m_session, &SessionController::steamHelpRequested, this, &SteamMaintenanceController::showHelp);
    connect(m_session, &SessionController::activeGameChanged, this, [this](const GameInfo&) { onActiveGameChanged(); });
    connect(m_grpc, &GrpcClient::vfsStatusReceived, this, &SteamMaintenanceController::onStatus);
    connect(m_grpc, &GrpcClient::vfsStatusChanged, this, &SteamMaintenanceController::onStatus);
    connect(m_grpc, &GrpcClient::steamMaintenanceSet, this, &SteamMaintenanceController::onMaintenanceSet);
    connect(m_grpc, &GrpcClient::steamMaintenanceSetFailed, this, &SteamMaintenanceController::onMaintenanceSetFailed);
    connect(m_grpc, &GrpcClient::preservedFilesImported, this, &SteamMaintenanceController::onFilesImported);
    connect(m_grpc, &GrpcClient::preservedFilesImportFailed, this, &SteamMaintenanceController::onFilesImportFailed);
    connect(m_grpc, &GrpcClient::preservedBatchDeleted, this, &SteamMaintenanceController::onBatchDeleted);
    connect(m_grpc, &GrpcClient::preservedBatchDeleteFailed, this, &SteamMaintenanceController::onBatchDeleteFailed);
    connect(m_grpc, &GrpcClient::vfsStatusQueried, this, [this](quint64 requestId, const GrpcVFSStatus&) {
        if (requestId == m_refreshRequestId) {
            m_refreshRequestId = 0;
            updateActions();
        }
    });
    connect(m_grpc, &GrpcClient::vfsStatusQueryFailed, this,
            [this](quint64 requestId, const QString& gameId, const QString& error) {
        if (requestId != m_refreshRequestId)
            return;
        m_refreshRequestId = 0;
        if (gameId == m_gameId)
            m_statusBar->showMessage(errorSummary("check saved files", error), 5000);
        updateActions();
    });
    connect(m_grpc, &GrpcClient::disconnected, this, [this] {
        m_haveStatus = false;
        m_maintenanceRequestId = 0;
        m_importRequestId = 0;
        m_deleteRequestId = 0;
        m_refreshRequestId = 0;
        m_operationGameId.clear();
        m_promptedGames.clear();
        m_panel->hide();
        m_savedDialog->hide();
        updateActions();
    });
    onActiveGameChanged();
}

void SteamMaintenanceController::onActiveGameChanged()
{
    const QString gameId = m_session->activeGame().detected ? m_session->activeGame().shortName : QString();
    if (m_gameId != gameId) {
        m_gameId = gameId;
        m_status = GrpcVFSStatus{};
        m_haveStatus = false;
        m_refreshRequestId = 0;
        m_panel->hide();
        m_savedDialog->hide();
    }
    updateActions();
}

void SteamMaintenanceController::onStatus(const GrpcVFSStatus& status)
{
    if (!m_grpc->isConnected() || status.gameId.isEmpty() || status.gameId != m_gameId)
        return;
    m_status = status;
    m_haveStatus = true;
    if (status.steamMaintenance != GrpcSteamMaintenanceState::VerifyRequired)
        m_promptedGames.remove(m_gameId);
    else if (!m_promptedGames.contains(m_gameId)) {
        m_promptedGames.insert(m_gameId);
        showHelp();
    }
    updateActions();
    if (m_savedDialog->isVisible())
        refreshSavedFiles();
}

void SteamMaintenanceController::updateActions()
{
    const bool ready = m_grpc->isConnected() && !m_gameId.isEmpty() && m_haveStatus;
    const bool busy = m_maintenanceRequestId || m_importRequestId || m_deleteRequestId || m_refreshRequestId;
    m_helpAction->setEnabled(ready);
    const bool verifyMounted = m_status.steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
        && m_status.mounted;
    m_pauseAction->setEnabled(ready && !busy && (m_status.steamMaintenance == GrpcSteamMaintenanceState::None
                                                 || verifyMounted));
    m_pauseButton->setVisible(verifyMounted);
    m_pauseButton->setEnabled(ready && !busy);
    m_finishButton->setVisible((m_status.steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
                                && !m_status.mounted)
                               || m_status.steamMaintenance == GrpcSteamMaintenanceState::UserRequested);
    m_finishButton->setText(m_status.steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
                                ? "Verification Finished" : "Steam Finished");
    m_finishButton->setEnabled(ready && !busy);
    m_showFilesButton->setVisible(m_status.steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
                                  || (!m_status.preservedBatches.empty()
                                      && m_status.steamMaintenance != GrpcSteamMaintenanceState::SteamBusy));
    m_showFilesButton->setEnabled(ready && !busy);
    m_openButton->setEnabled(ready && selectedBatch());
    const bool canChangeSavedFiles = ready && !busy && selectedBatch()
        && m_status.steamMaintenance != GrpcSteamMaintenanceState::SteamBusy;
    m_recoverButton->setEnabled(canChangeSavedFiles && m_files->count() > 0);
    m_deleteButton->setEnabled(canChangeSavedFiles);
    switch (m_status.steamMaintenance) {
    case GrpcSteamMaintenanceState::VerifyRequired:
        m_explanation->setText(verifyMounted
            ? "Steam changed game files while your mods were active. Choose Pause Mods to save Steam's changes and put the original game files back. Then verify the game in Steam."
            : "Steam changed game files while mods were active. Your changed files were saved, and your mods are paused. In Steam, right-click the game, choose Properties → Installed Files → Verify integrity of game files. When Steam has finished, choose Verification Finished.");
        break;
    case GrpcSteamMaintenanceState::UserRequested:
        m_explanation->setText("Mods are paused so Steam can update or check the game. Choose Steam Finished when Steam is done.");
        break;
    case GrpcSteamMaintenanceState::SteamBusy:
        m_explanation->setText("Steam is updating or checking this game. Mods stay as they are until Steam has finished.");
        break;
    default:
        m_explanation->setText(m_status.preservedBatches.empty()
                                   ? "No Steam update needs your attention for this game."
                                   : "Files Steam changed earlier are still saved.");
        break;
    }
}

void SteamMaintenanceController::showHelp()
{
    if (!m_grpc->isConnected() || !m_haveStatus || m_gameId.isEmpty())
        return;
    updateActions();
    m_panel->show();
    m_panel->raise();
    m_panel->activateWindow();
}

void SteamMaintenanceController::pauseMods()
{
    if (!m_pauseAction->isEnabled())
        return;
    const QString gameId = m_gameId;
    const bool verifyMounted = m_status.steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
        && m_status.mounted;
    if (!dialogs::plainConfirm(m_parentWindow, "Pause Mods for Steam",
            verifyMounted
                ? "Save Steam's changes and put the original game files back? Verify the game in Steam afterwards."
                : "Deactivate mods so Steam can update or verify the game safely? You can activate them again afterwards with Steam Finished."))
        return;
    if (m_gameId != gameId || !m_pauseAction->isEnabled())
        return;
    m_operationGameId = gameId;
    m_finishing = false;
    m_maintenanceRequestId = m_grpc->setSteamMaintenance(gameId, true, false);
    m_statusBar->showMessage("Pausing mods for Steam…");
    updateActions();
}

void SteamMaintenanceController::finishSteam()
{
    if (!m_finishButton->isEnabled() || !m_finishButton->isVisible())
        return;
    m_operationGameId = m_gameId;
    m_finishing = true;
    m_maintenanceRequestId = m_grpc->setSteamMaintenance(m_gameId, false, true);
    m_statusBar->showMessage("Checking that Steam has finished…");
    updateActions();
}

void SteamMaintenanceController::onMaintenanceSet(quint64 requestId, const GrpcVFSStatus& status)
{
    if (requestId != m_maintenanceRequestId || status.gameId != m_operationGameId)
        return;
    const bool finished = m_finishing;
    m_maintenanceRequestId = 0;
    m_operationGameId.clear();
    if (status.gameId == m_gameId) {
        onStatus(status);
        m_statusBar->showMessage(finished ? "Mods can be activated again." : "Mods are paused for Steam.", 5000);
        if (!finished)
            showHelp();
    }
    requestRefresh(status.gameId);
}

void SteamMaintenanceController::onMaintenanceSetFailed(quint64 requestId, const QString& gameId, const QString& error)
{
    if (requestId != m_maintenanceRequestId || gameId != m_operationGameId)
        return;
    m_maintenanceRequestId = 0;
    m_operationGameId.clear();
    presentError(m_parentWindow, "Steam Update Help", m_finishing ? "finish the Steam update" : "pause mods", error, true);
    requestRefresh(gameId);
}

void SteamMaintenanceController::showSavedFiles()
{
    if (!m_showFilesButton->isEnabled() || !m_showFilesButton->isVisible())
        return;
    refreshSavedFiles();
    m_savedDialog->show();
    m_savedDialog->raise();
    m_savedDialog->activateWindow();
}

void SteamMaintenanceController::refreshSavedFiles()
{
    const QString previous = m_batches->currentItem() ? m_batches->currentItem()->data(Qt::UserRole).toString() : QString();
    bool changed = m_batches->count() != static_cast<int>(m_status.preservedBatches.size());
    if (!changed) {
        for (int i = 0; i < m_batches->count(); ++i) {
            if (m_batches->item(i)->data(Qt::UserRole).toString() != m_status.preservedBatches[i].batchId
                || m_batches->item(i)->data(Qt::UserRole + 1).toString() != m_status.preservedBatches[i].path
                || m_batches->item(i)->data(Qt::UserRole + 2).toString() != m_status.preservedBatches[i].createdAt
                || m_batches->item(i)->data(Qt::UserRole + 3).toInt() != m_status.preservedBatches[i].fileCount) {
                changed = true;
                break;
            }
        }
    }
    if (!changed) {
        if (m_batches->count() == 0)
            showBatchFiles();
        else
            updateActions();
        return;
    }
    m_batches->clear();
    int selectedRow = 0;
    int row = 0;
    for (const auto& batch : m_status.preservedBatches) {
        const QDate created = QDate::fromString(batch.createdAt.left(10), Qt::ISODate);
        const QString date = created.isValid() ? QLocale().toString(created, QLocale::ShortFormat)
                                               : batch.createdAt.left(10);
        auto* item = new QListWidgetItem(QString("%1 — %2 files").arg(date).arg(batch.fileCount), m_batches);
        item->setData(Qt::UserRole, batch.batchId);
        item->setData(Qt::UserRole + 1, batch.path);
        item->setData(Qt::UserRole + 2, batch.createdAt);
        item->setData(Qt::UserRole + 3, batch.fileCount);
        if (batch.batchId == previous)
            selectedRow = row;
        ++row;
    }
    m_batches->setCurrentRow(m_batches->count() ? selectedRow : -1);
    showBatchFiles();
}

const GrpcPreservedBatch* SteamMaintenanceController::selectedBatch() const
{
    const auto* item = m_batches->currentItem();
    if (!item)
        return nullptr;
    const QString id = item->data(Qt::UserRole).toString();
    for (const auto& batch : m_status.preservedBatches)
        if (batch.batchId == id)
            return &batch;
    return nullptr;
}

void SteamMaintenanceController::showBatchFiles()
{
    m_files->clear();
    const auto* batch = selectedBatch();
    m_fileMessage->clear();
    if (!batch) {
        m_fileMessage->setText("There are no saved files for this game.");
        updateActions();
        return;
    }
    QFile file(QDir(batch->path).filePath("batch.json"));
    if (!file.open(QIODevice::ReadOnly)) {
        m_fileMessage->setText("Couldn't read the saved file list. The saved files have not been changed.");
        updateActions();
        return;
    }
    QJsonParseError error;
    const QJsonDocument document = QJsonDocument::fromJson(file.readAll(), &error);
    const auto files = document.object().value("files");
    if (error.error != QJsonParseError::NoError || !document.isObject() || !files.isArray()) {
        m_fileMessage->setText("Couldn't read the saved file list. The saved files have not been changed.");
        updateActions();
        return;
    }
    for (const auto& value : files.toArray()) {
        if (!value.isString()) {
            m_files->clear();
            m_fileMessage->setText("Couldn't read the saved file list. The saved files have not been changed.");
            break;
        }
        auto* item = new QListWidgetItem(value.toString(), m_files);
        item->setData(Qt::UserRole, value.toString());
    }
    updateActions();
}

void SteamMaintenanceController::openBatchFolder()
{
    const auto* batch = selectedBatch();
    if (!batch || !m_grpc->isConnected())
        return;
    const QString path = QDir(batch->path).filePath("files");
    if (!QFileInfo(path).isAbsolute() || !QFileInfo(path).isDir()
        || !QDesktopServices::openUrl(QUrl::fromLocalFile(path)))
        dialogs::plainWarn(m_savedDialog, "Saved Files", "Couldn't open the saved files folder.");
}

void SteamMaintenanceController::recoverFiles()
{
    const auto* batch = selectedBatch();
    if (!m_recoverButton->isEnabled() || !batch)
        return;
    const QString gameId = m_gameId;
    const QString batchId = batch->batchId;
    QStringList paths;
    for (const auto* item : m_files->selectedItems())
        paths.append(item->data(Qt::UserRole).toString());
    if (paths.isEmpty()) {
        for (int i = 0; i < m_files->count(); ++i)
            paths.append(m_files->item(i)->data(Qt::UserRole).toString());
    }
    const QDate created = QDate::fromString(batch->createdAt.left(10), Qt::ISODate);
    const QString date = created.isValid() ? created.toString(Qt::ISODate) : batch->createdAt.left(10);
    QInputDialog input(m_savedDialog);
    input.setWindowTitle("Recover Saved Files");
    input.setLabelText("Selected files become a new mod that starts disabled. Enable it only if you know you need these files.\n\nNew mod name:");
    input.setTextValue(QString("Steam changes %1").arg(date));
    if (input.exec() != QDialog::Accepted)
        return;
    const QString name = input.textValue().trimmed();
    if (name.isEmpty()) {
        dialogs::plainWarn(m_savedDialog, "Recover Saved Files", "Enter a name for the new mod.");
        return;
    }
    if (gameId != m_gameId || !m_recoverButton->isEnabled()
        || !selectedBatch() || selectedBatch()->batchId != batchId)
        return;
    m_operationGameId = gameId;
    m_importRequestId = m_grpc->importPreservedFiles(gameId, batchId, name, paths);
    updateActions();
}

void SteamMaintenanceController::onFilesImported(quint64 requestId, const QString& gameId,
                                                  const QString& modName, int fileCount)
{
    Q_UNUSED(fileCount)
    if (requestId != m_importRequestId || gameId != m_operationGameId)
        return;
    m_importRequestId = 0;
    m_operationGameId.clear();
    if (gameId == m_gameId)
        m_statusBar->showMessage(QString("Created the disabled mod \"%1\".").arg(modName), 5000);
    emit modRecovered(gameId);
    requestRefresh(gameId);
}

void SteamMaintenanceController::onFilesImportFailed(quint64 requestId, const QString& gameId, const QString& error)
{
    if (requestId != m_importRequestId || gameId != m_operationGameId)
        return;
    m_importRequestId = 0;
    m_operationGameId.clear();
    presentError(m_savedDialog, "Recover Saved Files", "recover saved files", error, true);
    requestRefresh(gameId);
}

void SteamMaintenanceController::deleteBatch()
{
    const auto* batch = selectedBatch();
    if (!m_deleteButton->isEnabled() || !batch)
        return;
    const QString gameId = m_gameId;
    const QString batchId = batch->batchId;
    if (!dialogs::plainConfirmDestructive(m_savedDialog, "Delete Saved Files",
            "Delete these saved files? This cannot be undone.", "Delete Saved Files"))
        return;
    if (gameId != m_gameId || !m_deleteButton->isEnabled()
        || !selectedBatch() || selectedBatch()->batchId != batchId)
        return;
    m_operationGameId = gameId;
    m_deleteRequestId = m_grpc->deletePreservedBatch(gameId, batchId);
    updateActions();
}

void SteamMaintenanceController::onBatchDeleted(quint64 requestId, const GrpcVFSStatus& status)
{
    if (requestId != m_deleteRequestId || status.gameId != m_operationGameId)
        return;
    m_deleteRequestId = 0;
    m_operationGameId.clear();
    if (status.gameId == m_gameId) {
        onStatus(status);
        m_statusBar->showMessage("Saved files deleted.", 5000);
    }
    requestRefresh(status.gameId);
}

void SteamMaintenanceController::onBatchDeleteFailed(quint64 requestId, const QString& gameId, const QString& error)
{
    if (requestId != m_deleteRequestId || gameId != m_operationGameId)
        return;
    m_deleteRequestId = 0;
    m_operationGameId.clear();
    presentError(m_savedDialog, "Delete Saved Files", "delete saved files", error, true);
    requestRefresh(gameId);
}

void SteamMaintenanceController::requestRefresh(const QString& gameId)
{
    if (gameId == m_gameId && m_grpc->isConnected())
        m_refreshRequestId = m_grpc->queryVfsStatus(gameId);
    updateActions();
}

}
