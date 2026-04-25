#pragma once

#include <QWidget>
#include <QTreeView>
#include <QSortFilterProxyModel>
#include <QCheckBox>
#include "GrpcClient.h"
#include "GameInfo.h"

namespace gorganizer {

class DownloadsModel;
class DownloadsRowDelegate;

// DownloadsLibraryView is the single Downloads tab: one row per archive,
// status cell progresses Downloading → Waiting → Installing → Installed
// in place. In-flight downloads without an archive yet live as transient
// rows keyed by downloadId; they promote to archive-keyed rows on the
// next daemon reconcile.
class DownloadsLibraryView : public QWidget {
    Q_OBJECT
public:
    explicit DownloadsLibraryView(GrpcClient* grpc, QWidget* parent = nullptr);

    void setGame(const GameInfo& game);

public slots:
    void refresh();

private slots:
    void onContextMenu(const QPoint& pos);
    void onDoubleClicked(const QModelIndex& idx);
    void onDownloadProgress(const GrpcDownloadProgress& progress);
    void onInstallProgress(const GrpcInstallProgress& progress);

signals:
    // Emitted after an install completes so MainWindow can refresh ModList.
    void modInstalledFromDownload();
    // Forwarded from the internal ModInstallDialog so MainWindow can wire
    // them into the InstallStatusBanner without the view knowing about it.
    void fomodWizardOpened(const QString& archivePath, const QString& modName);
    void fomodWizardClosed(const QString& archivePath);

private:
    GrpcClient* m_grpc;
    QTreeView* m_view;
    DownloadsModel* m_model;
    DownloadsRowDelegate* m_delegate;
    QSortFilterProxyModel* m_proxy;
    QCheckBox* m_autoInstallToggle;
    QCheckBox* m_showHiddenToggle = nullptr;
    GameInfo m_game;
    bool m_suppressToggleSignal = false;

    void actionInstall(const GrpcArchiveRow& row, bool forceNewMod);
    void actionMergeInto(const GrpcArchiveRow& row);
    void actionHide(const QString& archivePath, bool hidden);
    void actionBulkHide(GrpcBulkHideScope scope, bool hidden);
    void actionDelete(const GrpcArchiveRow& row);

    // Re-reads rows via ListArchives. Preserves transient in-flight rows.
    void reloadFromDaemon();

    // Converts a DownloadRowData (as stored in the model) into the
    // existing GrpcArchiveRow shape the action methods consume.
    static GrpcArchiveRow rowFromModel(const struct DownloadRowData& d);

    // Opens https://www.nexusmods.com/{domain}/mods/{modId}.
    static void openNexusPage(const GrpcArchiveRow& row);
};

} // namespace gorganizer
