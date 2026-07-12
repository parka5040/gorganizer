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
    void modInstalledFromDownload();
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

    // Re-reads rows via ListArchives, preserving transient in-flight rows.
    void reloadFromDaemon();

    static GrpcArchiveRow rowFromModel(const struct DownloadRowData& d);

    static void openNexusPage(const GrpcArchiveRow& row);
};

}
