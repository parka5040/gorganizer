#include "DownloadsLibraryView.h"
#include "DownloadsModel.h"
#include "DownloadsRowDelegate.h"
#include "ModInstallDialog.h"
#include "ThemeManager.h"
#include "Dialogs.h"

#include <QVBoxLayout>
#include <QHeaderView>
#include <QMenu>
#include <QMessageBox>
#include <QInputDialog>
#include <QPushButton>
#include <QDesktopServices>
#include <QUrl>
#include <QDir>
#include <QFileInfo>
#include <QSettings>
#include <QSortFilterProxyModel>

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

DownloadsLibraryView::DownloadsLibraryView(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
{
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
            dialogs::warn(this, "Settings Error", err);
    });
    header->addWidget(m_autoInstallToggle);

    layout->addLayout(header);

    m_model = new DownloadsModel(this);
    m_proxy = new DownloadsProxy(this);
    m_proxy->setSourceModel(m_model);
    m_proxy->setSortRole(Qt::DisplayRole);

    m_delegate = new DownloadsRowDelegate(this);

    m_view = new QTreeView;
    m_view->setModel(m_proxy);
    m_view->setItemDelegateForColumn(DownloadsModel::ColStatus, m_delegate);
    m_view->setRootIsDecorated(false);
    m_view->setAlternatingRowColors(true);
    m_view->setEditTriggers(QAbstractItemView::NoEditTriggers);
    m_view->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_view->setContextMenuPolicy(Qt::CustomContextMenu);
    m_view->setSortingEnabled(true);
    m_view->setUniformRowHeights(true);

    // The status delegate paints from the live theme palette; repaint on change.
    connect(ThemeManager::instance(), &ThemeManager::themeChanged, this,
            [this](const Palette&) {
                if (m_view)
                    m_view->viewport()->update();
            });

    QHeaderView* hdr = m_view->header();
    hdr->setSectionResizeMode(DownloadsModel::ColName,        QHeaderView::Interactive);
    hdr->setSectionResizeMode(DownloadsModel::ColVersion,     QHeaderView::Interactive);
    hdr->setSectionResizeMode(DownloadsModel::ColCategory,    QHeaderView::Interactive);
    hdr->setSectionResizeMode(DownloadsModel::ColStatus,      QHeaderView::Interactive);
    hdr->setSectionResizeMode(DownloadsModel::ColSize,        QHeaderView::Interactive);
    hdr->setSectionResizeMode(DownloadsModel::ColDownloaded,  QHeaderView::Interactive);
    hdr->setStretchLastSection(false);

    hdr->resizeSection(DownloadsModel::ColName,       540);
    hdr->resizeSection(DownloadsModel::ColVersion,    100);
    hdr->resizeSection(DownloadsModel::ColCategory,   140);
    hdr->resizeSection(DownloadsModel::ColStatus,     240);
    hdr->resizeSection(DownloadsModel::ColSize,       100);
    hdr->resizeSection(DownloadsModel::ColDownloaded, 160);

    {
        QSettings s;
        QByteArray saved = s.value("downloads/columns/headerState").toByteArray();
        if (!saved.isEmpty())
            hdr->restoreState(saved);
    }
    connect(hdr, &QHeaderView::sectionResized, this,
            [hdr](int, int, int) {
        QSettings s;
        s.setValue("downloads/columns/headerState", hdr->saveState());
    });

    connect(m_view, &QTreeView::customContextMenuRequested, this, &DownloadsLibraryView::onContextMenu);
    connect(m_view, &QTreeView::doubleClicked, this, &DownloadsLibraryView::onDoubleClicked);

    layout->addWidget(m_view);

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
                    dialogs::warn(this, "Refresh Failed", err);
                    return;
                }
                reloadFromDaemon();
            });
        }
        menu.addSeparator();
        menu.addAction("Open Nexus Page", this, [row] { openNexusPage(row); });
        if (!inFlight)
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
            QString modFolder, err;
            int fileCount = 0;
            if (!m_grpc->startInstallSync(m_game.shortName, row.archiveRelPath,
                                          QString(), GrpcInstallMergeIntoMod,
                                          existingFolder, QString(), {},
                                          modFolder, fileCount, err)) {
                dialogs::warn(this, "Merge Failed", err);
                return;
            }
            emit modInstalledFromDownload();
            reloadFromDaemon();
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
    QString modFolder, err;
    int fileCount = 0;
    GrpcInstallMode mode = GrpcInstallAsNewMod;
    QString target;

    if (!forceNewMod && row.status == 5 && !row.installedModFolder.isEmpty()) {
        mode = GrpcInstallMergeIntoMod;
        target = row.installedModFolder;
    }

    if (!m_grpc->startInstallSync(m_game.shortName, row.archiveRelPath, QString(),
                                  mode, target, QString(), {},
                                  modFolder, fileCount, err)) {
        if (err.contains("fomod_required")) {
            QString modsDir = GameInfo::modsDirPathFor(m_game.shortName);
            QString archiveAbs = modsDir + "/Downloads/" + row.archiveRelPath;
            QString defaultModName = row.modName.isEmpty()
                ? QFileInfo(row.fileArchiveName).completeBaseName()
                : row.modName;
            ModInstallDialog dlg(archiveAbs, modsDir, defaultModName, this);
            dlg.setDaemonContext(m_grpc, m_game.shortName);
            connect(&dlg, &ModInstallDialog::fomodWizardOpened,
                    this, &DownloadsLibraryView::fomodWizardOpened);
            connect(&dlg, &ModInstallDialog::fomodWizardClosed,
                    this, &DownloadsLibraryView::fomodWizardClosed);
            if (dlg.exec() == QDialog::Accepted)
                emit modInstalledFromDownload();
            reloadFromDaemon();
            return;
        }
        dialogs::warn(this, "Install Failed", err);
        return;
    }
    emit modInstalledFromDownload();
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

    bool ok = false;
    QString target = QInputDialog::getItem(this, "Merge Into Existing Mod",
        "Target mod folder:", candidates, 0, true, &ok);
    if (!ok || target.isEmpty())
        return;

    QString modFolder, err;
    int fileCount = 0;
    if (!m_grpc->startInstallSync(m_game.shortName, row.archiveRelPath, QString(),
                                  GrpcInstallMergeIntoMod, target, QString(), {},
                                  modFolder, fileCount, err)) {
        dialogs::warn(this, "Merge Failed", err);
        return;
    }
    emit modInstalledFromDownload();
    reloadFromDaemon();
}

void DownloadsLibraryView::actionHide(const QString& archivePath, bool hidden)
{
    QString err;
    if (!m_grpc->setArchiveHidden(m_game.shortName, archivePath, hidden, err)) {
        dialogs::warn(this, "Hide Failed", err);
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
        dialogs::warn(this, "Bulk Hide Failed", err);
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
    if (!m_grpc->removeArchive(m_game.shortName, row.archiveRelPath, err)) {
        dialogs::warn(this, "Delete Failed", err);
        return;
    }
    m_model->removeByKey(row.archiveRelPath);
}

void DownloadsLibraryView::openNexusPage(const GrpcArchiveRow& row)
{
    if (row.gameDomain.isEmpty() || row.modId == 0)
        return;
    QString url = QString("https://www.nexusmods.com/%1/mods/%2")
                      .arg(row.gameDomain).arg(row.modId);
    QDesktopServices::openUrl(QUrl(url));
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
