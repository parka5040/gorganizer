#pragma once

#include <QWidget>
#include <QHash>
#include <QPointer>
#include <QTreeView>
#include <QSortFilterProxyModel>
#include <QCheckBox>
#include "GrpcClient.h"
#include "GameInfo.h"

class QProgressDialog;
class QDragEnterEvent;
class QDragMoveEvent;
class QDropEvent;
class QLabel;

namespace gorganizer {

class DownloadsModel;
class DownloadsRowDelegate;
class InstallController;

class DownloadsLibraryView : public QWidget {
    Q_OBJECT
public:
    explicit DownloadsLibraryView(GrpcClient* grpc, InstallController* installs, QWidget* parent = nullptr);

    void setGame(const GameInfo& game);

public slots:
    void refresh();

private slots:
    void onContextMenu(const QPoint& pos);
    void onDoubleClicked(const QModelIndex& idx);
    void onDownloadProgress(const GrpcDownloadProgress& progress);
    void onInstallProgress(const GrpcInstallProgress& progress);
    void onInstallSucceeded(quint64 requestId, const QString& modFolder, int fileCount);
    void onInstallFailed(quint64 requestId, const QString& error);
    void onInstallCancelled(quint64 requestId);
    void onInstallUnknown(quint64 requestId);

signals:
    void modInstalledFromDownload(const QString& gameId);
    void modStateNeedsRefresh(const QString& gameId);
    void archivesDropped(const QStringList& paths, const QStringList& rejected);
    void archivesRejected(const QStringList& rejected);
    void fomodWizardOpened(const QString& archivePath, const QString& modName);
    void fomodWizardClosed(const QString& archivePath);

protected:
    void dragEnterEvent(QDragEnterEvent* event) override;
    void dragMoveEvent(QDragMoveEvent* event) override;
    void dropEvent(QDropEvent* event) override;

private:
    struct Attempt {
        GrpcArchiveRow row;
        QString gameId;
        QString target;
        GrpcInstallMode mode = GrpcInstallAsNewMod;
        bool explicitMerge = false;
        QPointer<QProgressDialog> progress;
    };
    void finishAttempt(quint64 requestId);
    void updateEmptyState();

    GrpcClient* m_grpc;
    InstallController* m_installs;
    QHash<quint64, Attempt> m_attempts;
    QTreeView* m_view;
    QLabel* m_emptyLabel;
    DownloadsModel* m_model;
    DownloadsRowDelegate* m_delegate;
    QSortFilterProxyModel* m_proxy;
    QCheckBox* m_autoInstallToggle;
    QCheckBox* m_showHiddenToggle = nullptr;
    GameInfo m_game;
    bool m_suppressToggleSignal = false;

    void actionInstall(const GrpcArchiveRow& row, bool forceNewMod);
    void installArchive(const GrpcArchiveRow& row, GrpcInstallMode mode, QString target,
                        bool explicitMerge = false);
    void actionMergeInto(const GrpcArchiveRow& row);
    void showFomodInstallDialog(const GrpcArchiveRow& row, GrpcInstallMode mode, const QString& target);
    void actionHide(const QString& archivePath, bool hidden);
    void actionBulkHide(GrpcBulkHideScope scope, bool hidden);
    void actionDelete(const GrpcArchiveRow& row);

    // Re-reads rows via ListArchives, preserving transient in-flight rows.
    void reloadFromDaemon();

    static GrpcArchiveRow rowFromModel(const struct DownloadRowData& d);

    void openNexusPage(const GrpcArchiveRow& row);
};

}
