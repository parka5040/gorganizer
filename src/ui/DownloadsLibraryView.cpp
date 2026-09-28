#include "DownloadsLibraryView.h"
#include "ArchiveDrop.h"
#include "DownloadsModel.h"
#include "DownloadsRowDelegate.h"
#include "ModInstallDialog.h"
#include "InstallController.h"
#include "ThemeManager.h"
#include "Dialogs.h"
#include "InstallErrorText.h"
#include "InstallCollisionDialog.h"
#include "ErrorPresenter.h"
#include "SafeLinks.h"

#include <QVBoxLayout>
#include <QHeaderView>
#include <QMenu>
#include <QMessageBox>
#include <QInputDialog>
#include <QPushButton>
#include <QProgressDialog>
#include <QTimer>
#include <QDir>
#include <QFileInfo>
#include <QSettings>
#include <QSortFilterProxyModel>
#include <QLabel>
#include <QDragEnterEvent>
#include <QDragMoveEvent>
#include <QDropEvent>
#include <QMimeData>
#include <QShowEvent>

namespace gorganizer {

class DownloadsProxy : public QSortFilterProxyModel {
public:
    using QSortFilterProxyModel::QSortFilterProxyModel;

    void setShowHidden(bool show)
    {
        auto* src = qobject_cast<DownloadsModel*>(sourceModel());
        if (!src) return;
        src->setShowHidden(show);
        invalidateFilter();
    }

protected:
    bool filterAcceptsRow(int sourceRow, const QModelIndex&) const override
    {
        auto* src = qobject_cast<DownloadsModel*>(sourceModel());
        return src ? src->rowMatchesFilter(sourceRow) : true;
    }
};

DownloadsLibraryView::DownloadsLibraryView(GrpcClient* grpc, InstallController* installs, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
    , m_installs(installs)
{
    setAcceptDrops(true);
    auto* layout = new QVBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);

    auto* header = new QHBoxLayout;
    header->setContentsMargins(4, 4, 4, 0);
    m_showHiddenToggle = new QCheckBox("Show Hidden");
    m_showHiddenToggle->setToolTip(
        "Include archives you've previously hidden in this list.\n"
        "Same as right-clicking and toggling 'Show Hidden'.");
    connect(m_showHiddenToggle, &QCheckBox::toggled, this, [this](bool checked) {
        static_cast<DownloadsProxy*>(m_proxy)->setShowHidden(checked);
    });
    header->addWidget(m_showHiddenToggle);
    header->addStretch();

    m_autoInstallToggle = new QCheckBox("Auto-install downloaded mods for this game");
    m_autoInstallToggle->setToolTip(
        "When enabled, completed downloads with a recognized layout install automatically.\n"
        "Disable to always double-click in this list to install.");
    connect(m_autoInstallToggle, &QCheckBox::toggled, this, [this](bool checked) {
        if (m_suppressToggleSignal || m_game.shortName.isEmpty())
            return;
        GrpcGameSettings s;
        QString err;
        if (!m_grpc->setGameSettings(m_game.shortName, checked, s, err))
            presentError(this, "Settings Error", "save settings", err, true);
    });
    header->addWidget(m_autoInstallToggle);

    layout->addLayout(header);

    m_model = new DownloadsModel(this);
    m_proxy = new DownloadsProxy(this);
    m_proxy->setSourceModel(m_model);
    m_proxy->setSortRole(Qt::DisplayRole);

    m_delegate = new DownloadsRowDelegate(this);

    m_view = new QTreeView;
    m_view->setAcceptDrops(false);
    m_view->viewport()->setAcceptDrops(false);
    m_view->setModel(m_proxy);
    m_view->setItemDelegateForColumn(DownloadsModel::ColStatus, m_delegate);
    m_view->setRootIsDecorated(false);
    m_view->setAlternatingRowColors(true);
    m_view->setEditTriggers(QAbstractItemView::NoEditTriggers);
    m_view->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_view->setContextMenuPolicy(Qt::CustomContextMenu);
    m_view->setSortingEnabled(true);
    m_view->setUniformRowHeights(true);

    connect(ThemeManager::instance(), &ThemeManager::themeChanged, this,
            [this](const Palette&) {
                if (m_view)
                    m_view->viewport()->update();
            });

    QHeaderView* hdr = m_view->header();
    hdr->setSectionResizeMode(QHeaderView::Interactive);
    hdr->setStretchLastSection(false);

    {
        QSettings s;
        QByteArray saved = s.value("downloads/columns/headerState").toByteArray();
        if (!saved.isEmpty() && hdr->restoreState(saved))
            m_fitColumnsOnFirstShow = false;
    }
    hdr->setSectionResizeMode(DownloadsModel::ColName, QHeaderView::Stretch);
    connect(hdr, &QHeaderView::sectionResized, this,
            [hdr](int, int, int) {
        QSettings s;
        s.setValue("downloads/columns/headerState", hdr->saveState());
    });

    connect(m_view, &QTreeView::customContextMenuRequested, this, &DownloadsLibraryView::onContextMenu);
    connect(m_view, &QTreeView::doubleClicked, this, &DownloadsLibraryView::onDoubleClicked);

    layout->addWidget(m_view, 1);
    m_emptyLabel = new QLabel("Drop mod archives here to install them.");
    m_emptyLabel->setAlignment(Qt::AlignCenter);
    m_emptyLabel->setObjectName("hintLabel");
    layout->addWidget(m_emptyLabel, 1);
    connect(m_proxy, &QAbstractItemModel::modelReset, this, &DownloadsLibraryView::updateEmptyState);
    connect(m_proxy, &QAbstractItemModel::rowsInserted, this, &DownloadsLibraryView::updateEmptyState);
    connect(m_proxy, &QAbstractItemModel::rowsRemoved, this, &DownloadsLibraryView::updateEmptyState);
    updateEmptyState();

    connect(m_grpc, &GrpcClient::archiveEventReceived, this,
            [this](const GrpcArchiveEvent& evt) {
        switch (evt.kind) {
        case GrpcArchiveEvent::KindDownloadProgress:
            onDownloadProgress(evt.progress);
            break;
        case GrpcArchiveEvent::KindRowChanged:
            if (!evt.row.downloadId.isEmpty())
                m_model->removeTransientByDownloadId(evt.row.downloadId);
            reloadFromDaemon();
            break;
        case GrpcArchiveEvent::KindArchiveRemoved:
            m_model->removeByKey(evt.archiveRemoved);
            break;
        default:
            break;
        }
    });
    connect(m_grpc, &GrpcClient::installProgressEvent,
            this, &DownloadsLibraryView::onInstallProgress);
    connect(m_grpc, &GrpcClient::resubscribed, this, &DownloadsLibraryView::reloadFromDaemon);
    connect(m_installs, &InstallController::installSucceeded, this, &DownloadsLibraryView::onInstallSucceeded);
    connect(m_installs, &InstallController::installFailed, this, &DownloadsLibraryView::onInstallFailed);
    connect(m_installs, &InstallController::cancelled, this, &DownloadsLibraryView::onInstallCancelled);
    connect(m_installs, &InstallController::outcomeUnknown, this, &DownloadsLibraryView::onInstallUnknown);
    connect(m_installs, &InstallController::reconciling, this, [this](quint64 id) {
        auto it = m_attempts.find(id);
        if (it != m_attempts.end() && it->progress) {
            it->progress->setCancelButton(nullptr);
            it->progress->setLabelText("Checking whether this archive was installed…");
        }
    });
}

void DownloadsLibraryView::showEvent(QShowEvent* event)
{
    QWidget::showEvent(event);
    if (!m_fitColumnsOnFirstShow)
        return;
    m_fitColumnsOnFirstShow = false;
    QHeaderView* hdr = m_view->header();
    for (int col = DownloadsModel::ColVersion; col <= DownloadsModel::ColDownloaded; ++col) {
        m_view->resizeColumnToContents(col);
        const int limit = col == DownloadsModel::ColStatus ? 150 : 120;
        hdr->resizeSection(col, qMin(hdr->sectionSize(col), limit));
    }
}

void DownloadsLibraryView::dragEnterEvent(QDragEnterEvent* event)
{
    const ArchiveDrop drop = inspectArchiveDrop(event->mimeData());
    if (drop.paths.isEmpty()) {
        if (!drop.rejected.isEmpty())
            emit archivesRejected(drop.rejected);
        event->ignore();
        return;
    }
    event->setDropAction(Qt::CopyAction);
    event->accept();
}

void DownloadsLibraryView::dragMoveEvent(QDragMoveEvent* event)
{
    if (inspectArchiveDrop(event->mimeData()).paths.isEmpty()) {
        event->ignore();
        return;
    }
    event->setDropAction(Qt::CopyAction);
    event->accept();
}

void DownloadsLibraryView::dropEvent(QDropEvent* event)
{
    const ArchiveDrop drop = inspectArchiveDrop(event->mimeData());
    if (drop.paths.isEmpty()) {
        event->ignore();
        if (!drop.rejected.isEmpty())
            emit archivesRejected(drop.rejected);
        return;
    }
    event->setDropAction(Qt::CopyAction);
    event->accept();
    emit archivesDropped(drop.paths, drop.rejected);
}

void DownloadsLibraryView::updateEmptyState()
{
    const bool empty = m_proxy->rowCount() == 0;
    m_view->setVisible(!empty);
    m_emptyLabel->setVisible(empty);
}

void DownloadsLibraryView::setGame(const GameInfo& game)
{
    m_game = game;

    if (!game.shortName.isEmpty()) {
        GrpcGameSettings s;
        QString err;
        m_suppressToggleSignal = true;
        if (m_grpc->getGameSettings(game.shortName, s, err))
            m_autoInstallToggle->setChecked(s.autoInstall);
        else
            m_autoInstallToggle->setChecked(false);
        m_suppressToggleSignal = false;
    }

    reloadFromDaemon();
}

void DownloadsLibraryView::refresh()
{
    reloadFromDaemon();
}

void DownloadsLibraryView::reloadFromDaemon()
{
    if (m_game.shortName.isEmpty()) {
        m_model->replaceFromDaemon({});
        return;
    }
    std::vector<GrpcArchiveRow> rows;
    QString err;
    if (!m_grpc->listArchives(m_game.shortName, rows, err)) {
        m_model->replaceFromDaemon({});
        return;
    }
    m_model->replaceFromDaemon(rows);
}

GrpcArchiveRow DownloadsLibraryView::rowFromModel(const DownloadRowData& d)
{
    GrpcArchiveRow r;
    r.archiveRelPath = d.archiveRelPath;
    r.modId = d.modId;
    r.fileId = d.fileId;
    r.modName = d.name;
    r.fileName = d.fileName;
    r.fileArchiveName = d.fileArchiveName;
    r.version = d.version;
    r.category = d.category;
    r.sizeBytes = d.sizeBytes;
    r.uploadedAt = d.uploadedAt;
    r.downloadedAt = d.downloadedAt;
    r.hidden = d.hidden;
    r.gameDomain = d.gameDomain;
    r.thumbnailUrl = d.thumbnailUrl;
    r.adultContent = d.adultContent;
    r.status = static_cast<int>(d.phase);
    r.downloadId = d.downloadId;
    r.installedModFolder = d.installedModFolder;
    return r;
}

void DownloadsLibraryView::onContextMenu(const QPoint& pos)
{
    QMenu menu(this);
    QModelIndex proxyIdx = m_view->indexAt(pos);
    QModelIndex srcIdx = proxyIdx.isValid() ? m_proxy->mapToSource(proxyIdx) : QModelIndex{};

    if (srcIdx.isValid()) {
        DownloadRowData d = m_model->rowAt(srcIdx.row());
        GrpcArchiveRow row = rowFromModel(d);
        bool isInstalled = (d.phase == DownloadPhase::Installed);
        bool inFlight = (d.phase == DownloadPhase::Queued ||
                         d.phase == DownloadPhase::Downloading);
        bool retryable = (d.phase == DownloadPhase::Failed ||
                          d.phase == DownloadPhase::Cancelled);

        if (inFlight) {
            QString dlId = d.downloadId;
            menu.addAction("Cancel Download", this, [this, dlId] {
                if (!dlId.isEmpty()) m_grpc->cancelDownload(dlId);
            });
            menu.addSeparator();
        } else if (retryable) {
            QString dlId = d.downloadId;
            menu.addAction("Retry Download", this, [this, dlId] {
                if (!dlId.isEmpty()) m_grpc->retryDownload(dlId);
            });
            menu.addAction("Remove From List", this, [this, row] {
                if (!dialogs::confirm(this, "Remove Download",
                    "Remove this download from the list? Any partly downloaded file is deleted."))
                    return;
                QString err;
                if (!m_grpc->removeArchive(m_game.shortName, row.archiveRelPath, row.downloadId, err)) {
                    presentError(this, "Remove Failed", "remove this download", err, true);
                    return;
                }
                m_model->removeTransientByDownloadId(row.downloadId);
                reloadFromDaemon();
            });
            menu.addSeparator();
        }

        if (!inFlight && !retryable) {
            menu.addAction(isInstalled ? "Reinstall" : "Install", this,
                           [this, row] { actionInstall(row, false); });
            menu.addAction("Install As New Mod...", this,
                           [this, row] { actionInstall(row, true); });
            menu.addAction("Merge Into Existing Mod...", this,
                           [this, row] { actionMergeInto(row); });
            if (d.merged && !d.installedModFolder.isEmpty()) {
                QString folder = d.installedModFolder;
                menu.addAction("Show Containing Mod", this, [this, folder] {
                    dialogs::info(this, "Merged Into",
                        QString("This archive was merged into:\n\n%1\n\n"
                                "Open the Mods tab to inspect or rearrange it.")
                            .arg(folder));
                });
            }
            menu.addSeparator();
        }
        menu.addAction(row.hidden ? "Un-Hide" : "Hide", this,
                       [this, row] { actionHide(row.archiveRelPath, !row.hidden); });
        if (!inFlight) {
            menu.addAction("Refresh Nexus Metadata", this, [this, row] {
                QString err;
                GrpcArchiveRow fresh;
                if (!m_grpc->refreshArchiveMetadata(m_game.shortName, row.archiveRelPath, fresh, err)) {
                    presentError(this, "Refresh Failed", "refresh this archive's information", err);
                    return;
                }
                reloadFromDaemon();
            });
        }
        menu.addSeparator();
        menu.addAction("Open Nexus Page", this, [this, row] { openNexusPage(row); });
        if (!inFlight && !retryable && !row.archiveRelPath.isEmpty())
            menu.addAction("Delete Archive", this, [this, row] { actionDelete(row); });
        menu.addSeparator();
    }

    menu.addAction("Hide All Installed", this,
                   [this] { actionBulkHide(GrpcBulkHideInstalled, true); });
    menu.addAction("Hide All Uninstalled", this,
                   [this] { actionBulkHide(GrpcBulkHideUninstalled, true); });
    menu.addAction("Un-Hide All", this,
                   [this] { actionBulkHide(GrpcBulkHideAll, false); });
    menu.addSeparator();

    auto* toggle = menu.addAction("Show Hidden");
    toggle->setCheckable(true);
    toggle->setChecked(m_model->showHidden());
    connect(toggle, &QAction::toggled, this, [this](bool checked) {
        if (m_showHiddenToggle && m_showHiddenToggle->isChecked() != checked)
            m_showHiddenToggle->setChecked(checked);
        else
            static_cast<DownloadsProxy*>(m_proxy)->setShowHidden(checked);
    });

    menu.exec(m_view->viewport()->mapToGlobal(pos));
}

void DownloadsLibraryView::onDoubleClicked(const QModelIndex& idx)
{
    if (!idx.isValid())
        return;
    QModelIndex srcIdx = m_proxy->mapToSource(idx);
    DownloadRowData d = m_model->rowAt(srcIdx.row());
    GrpcDownloadRow row = rowFromModel(d);

    QString existingFolder;
    if (d.phase != DownloadPhase::Installed && row.modId != 0) {
        int rc = m_model->rowCount();
        for (int r = 0; r < rc; ++r) {
            if (r == srcIdx.row())
                continue;
            DownloadRowData other = m_model->rowAt(r);
            if (other.phase == DownloadPhase::Installed && other.modId == row.modId
                && !other.installedModFolder.isEmpty()) {
                existingFolder = other.installedModFolder;
                break;
            }
        }
    }

    if (!existingFolder.isEmpty()) {
        QMessageBox box(this);
        box.setWindowTitle("Multi-Archive Mod");
        box.setIcon(QMessageBox::Question);
        box.setTextFormat(Qt::PlainText);
        QString displayName = row.modName.isEmpty() ? row.fileArchiveName : row.modName;
        box.setText(QString("An archive for \"%1\" is already installed as mod \"%2\".")
                        .arg(displayName, existingFolder));
        box.setInformativeText(
            "Install this archive as a new, separate mod, or merge it "
            "into the existing mod folder?\n\n"
            "Merging is typical when a mod ships meshes/textures/ESP as "
            "separate downloads for the same Nexus mod ID.");
        QAbstractButton* mergeBtn = box.addButton("Merge Into Existing", QMessageBox::AcceptRole);
        QAbstractButton* newBtn = box.addButton("Install As New", QMessageBox::ActionRole);
        QAbstractButton* cancelBtn = box.addButton(QMessageBox::Cancel);
        box.setDefaultButton(static_cast<QPushButton*>(mergeBtn));
        box.exec();

        QAbstractButton* clicked = box.clickedButton();
        if (clicked == cancelBtn)
            return;
        if (clicked == mergeBtn) {
            installArchive(row, GrpcInstallMergeIntoMod, existingFolder, true);
            return;
        }
        (void)newBtn;
        actionInstall(row, true);
        return;
    }

    actionInstall(row, false);
}

void DownloadsLibraryView::actionInstall(const GrpcArchiveRow& row, bool forceNewMod)
{
    const QString target = !forceNewMod && row.status == 5 ? row.installedModFolder : QString();
    installArchive(row, GrpcInstallAsNewMod, target);
}

void DownloadsLibraryView::installArchive(const GrpcArchiveRow& row, GrpcInstallMode mode,
                                          QString target, bool explicitMerge)
{
    if (m_game.shortName.isEmpty()) return;
    for (const auto& attempt : m_attempts) {
        if (attempt.gameId == m_game.shortName && attempt.row.archiveRelPath == row.archiveRelPath) {
            if (attempt.progress) attempt.progress->show();
            return;
        }
    }
    InstallController::InstallRequest request;
    request.gameId = m_game.shortName;
    request.archiveRelPath = row.archiveRelPath;
    request.mode = mode;
    request.targetMod = target;
    const quint64 id = m_installs->install(request);
    auto* progress = new QProgressDialog(QStringLiteral("Installing %1…").arg(row.fileArchiveName),
                                         QStringLiteral("Cancel Install"), 0, 0, this);
    progress->setWindowTitle(QStringLiteral("Installing Archive"));
    progress->setWindowModality(Qt::NonModal);
    progress->setMinimumDuration(0);
    progress->setAutoClose(false);
    connect(progress, &QProgressDialog::canceled, this, [this, id, progress] {
        m_installs->cancel(id);
        QTimer::singleShot(0, progress, [progress] {
            progress->setCancelButton(nullptr);
            progress->setLabelText("Cancelling… checking whether anything was installed.");
            progress->show();
        });
    });
    m_attempts.insert(id, Attempt{row, m_game.shortName, target, mode, explicitMerge, progress});
    progress->show();
}

void DownloadsLibraryView::finishAttempt(quint64 requestId)
{
    const auto attempt = m_attempts.take(requestId);
    if (attempt.progress) {
        attempt.progress->hide();
        attempt.progress->deleteLater();
    }
    if (m_game.shortName == attempt.gameId) reloadFromDaemon();
}

void DownloadsLibraryView::onInstallSucceeded(quint64 requestId, const QString&, int)
{
    if (!m_attempts.contains(requestId)) return;
    const QString gameId = m_attempts.value(requestId).gameId;
    finishAttempt(requestId);
    emit modInstalledFromDownload(gameId);
}

void DownloadsLibraryView::onInstallFailed(quint64 requestId, const QString& error)
{
    if (!m_attempts.contains(requestId)) return;
    const Attempt attempt = m_attempts.value(requestId);
    finishAttempt(requestId);
    if (attempt.gameId == m_game.shortName) {
        const QString token = parseInstallError(error).token;
        if (token == QLatin1String("fomod_required") && usesLocalDataRootInstall(m_game)) {
            showFomodInstallDialog(attempt.row, attempt.mode, attempt.target);
            return;
        }
        if (token == QLatin1String("mod_collision") && !attempt.explicitMerge) {
            const auto choice = resolveInstallCollision(this, error, attempt.target);
            if (choice) installArchive(attempt.row, choice->mode, choice->targetMod);
            return;
        }
    }
    presentError(this, attempt.explicitMerge ? "Merge Failed" : "Install Failed",
                 attempt.explicitMerge ? "merge this archive into the mod" : "install this mod", error, true);
}

void DownloadsLibraryView::onInstallCancelled(quint64 requestId)
{
    if (!m_attempts.contains(requestId)) return;
    finishAttempt(requestId);
    dialogs::info(this, "Install Cancelled", "Install cancelled. Nothing was installed.");
}

void DownloadsLibraryView::onInstallUnknown(quint64 requestId)
{
    if (!m_attempts.contains(requestId)) return;
    const QString gameId = m_attempts.value(requestId).gameId;
    finishAttempt(requestId);
    emit modStateNeedsRefresh(gameId);
    dialogs::plainWarn(this, "Install Result Unknown",
        "Gorganizer could not confirm whether this archive was installed. Check Mods and Downloads before trying again.");
}

void DownloadsLibraryView::showFomodInstallDialog(const GrpcArchiveRow& row,
                                                  GrpcInstallMode mode, const QString& target)
{
    QString defaultModName = row.modName.isEmpty()
        ? QFileInfo(row.fileArchiveName).completeBaseName()
        : row.modName;
    ModInstallDialog dlg(m_game.shortName, defaultModName, m_grpc, m_installs,
                         ModInstallDialog::ArchiveSource::fromLibrary(row.archiveRelPath),
                         this, {mode, target});
    connect(&dlg, &ModInstallDialog::fomodWizardOpened,
            this, &DownloadsLibraryView::fomodWizardOpened);
    connect(&dlg, &ModInstallDialog::fomodWizardClosed,
            this, &DownloadsLibraryView::fomodWizardClosed);
    if (dlg.exec() == QDialog::Accepted)
        emit modInstalledFromDownload(m_game.shortName);
    else if (dlg.installUnconfirmed())
        emit modStateNeedsRefresh(m_game.shortName);
    reloadFromDaemon();
}

void DownloadsLibraryView::actionMergeInto(const GrpcArchiveRow& row)
{
    QStringList candidates;
    int rc = m_model->rowCount();
    for (int r = 0; r < rc; ++r) {
        DownloadRowData d = m_model->rowAt(r);
        if (d.phase == DownloadPhase::Installed && d.modId == row.modId
            && !d.installedModFolder.isEmpty()
            && !candidates.contains(d.installedModFolder))
            candidates.append(d.installedModFolder);
    }
    if (candidates.isEmpty())
        candidates.append("<type a mod folder name>");

    QString target;
    for (;;) {
        bool ok = false;
        target = QInputDialog::getItem(this, "Merge Into Existing Mod",
            "Target mod folder:", candidates, 0, true, &ok).trimmed();
        if (!ok)
            return;
        const QString problem = modNameProblem(target);
        if (problem.isEmpty())
            break;
        dialogs::plainWarn(this, "Merge Into Existing Mod", problem);
    }

    installArchive(row, GrpcInstallMergeIntoMod, target, true);
}

void DownloadsLibraryView::actionHide(const QString& archivePath, bool hidden)
{
    QString err;
    if (!m_grpc->setArchiveHidden(m_game.shortName, archivePath, hidden, err)) {
        presentError(this, "Hide Failed", "hide this archive", err, true);
        return;
    }
    m_model->setHidden(archivePath, hidden);
    m_proxy->invalidate();
}

void DownloadsLibraryView::actionBulkHide(GrpcBulkHideScope scope, bool hidden)
{
    QString err;
    int affected = 0;
    if (!m_grpc->setArchivesHiddenBulk(m_game.shortName, hidden, scope, affected, err)) {
        presentError(this, "Bulk Hide Failed", "hide these archives", err, true);
        return;
    }
    reloadFromDaemon();
}

void DownloadsLibraryView::actionDelete(const GrpcArchiveRow& row)
{
    if (!dialogs::confirm(this, "Delete Archive",
        QString("Delete %1 from disk? This cannot be undone.").arg(row.fileArchiveName)))
        return;
    QString err;
    if (!m_grpc->removeArchive(m_game.shortName, row.archiveRelPath, row.downloadId, err)) {
        presentError(this, "Delete Failed", "delete this archive", err, true);
        return;
    }
    m_model->removeTransientByDownloadId(row.downloadId);
    reloadFromDaemon();
}

void DownloadsLibraryView::openNexusPage(const GrpcArchiveRow& row)
{
    if (row.gameDomain.isEmpty() || row.modId == 0)
        return;
    QString url = QString("https://www.nexusmods.com/%1/mods/%2")
                      .arg(row.gameDomain).arg(row.modId);
    openWebLink(this, url);
}

void DownloadsLibraryView::onDownloadProgress(const GrpcDownloadProgress& progress)
{
    m_model->applyDownloadProgress(progress);
    if (progress.status == 3 || progress.status >= 5)
        reloadFromDaemon();
}

void DownloadsLibraryView::onInstallProgress(const GrpcInstallProgress& progress)
{
    m_model->applyInstallProgress(progress);
    if (progress.step == GrpcInstallStepComplete || progress.step == GrpcInstallStepFailed) {
        reloadFromDaemon();
    }
}

}
