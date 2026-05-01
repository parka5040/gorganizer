#pragma once

#include <QAbstractTableModel>
#include <QHash>
#include <QVector>
#include <vector>

#include "GrpcClient.h"

namespace gorganizer {

// Phase of a download row. Values mirror the proto DownloadStatus enum.
enum class DownloadPhase {
    Unknown     = 0,
    Queued      = 1,
    Downloading = 2,
    Downloaded  = 3,
    Installing  = 4,
    Installed   = 5,
    Uninstalled = 6,
    Cancelled   = 7,
    Failed      = 8,
};

struct DownloadRowData {
    QString key;
    QString archiveRelPath;
    QString downloadId;
    QString name;
    QString fileName;
    QString fileArchiveName;
    int modId = 0;
    int fileId = 0;
    QString version;
    QString category;
    qint64 sizeBytes = 0;
    qint64 bytesDownloaded = 0;
    QString uploadedAt;
    QString downloadedAt;
    QString gameDomain;
    QString thumbnailUrl;
    bool adultContent = false;
    DownloadPhase phase = DownloadPhase::Unknown;
    int pct = -1;
    QString currentFile;
    qint64 filesDone = 0;
    qint64 filesTotal = 0;
    QString error;
    bool hidden = false;
    QString installedModFolder;
    bool merged = false;
};

// Table model keyed by archive relpath (or "dl:<downloadId>" for transient rows).
class DownloadsModel : public QAbstractTableModel {
    Q_OBJECT
public:
    enum Column {
        ColName = 0,
        ColVersion,
        ColCategory,
        ColStatus,
        ColSize,
        ColDownloaded,
        ColCount,
    };

    enum Roles {
        PhaseRole = Qt::UserRole + 1,
        ProgressRole,
        KeyRole,
        RowDataRole,
        HiddenRole,
        MergedRole,
    };

    explicit DownloadsModel(QObject* parent = nullptr);

    int rowCount(const QModelIndex& parent = {}) const override;
    int columnCount(const QModelIndex& parent = {}) const override;
    QVariant data(const QModelIndex& idx, int role) const override;
    QVariant headerData(int section, Qt::Orientation orientation, int role) const override;

    // Reconcile with daemon's ListDownloads, preserving transient in-flight rows by downloadId.
    void replaceFromDaemon(const std::vector<GrpcDownloadRow>& rows);

    void applyDownloadProgress(const GrpcDownloadProgress& p);
    void applyInstallProgress(const GrpcInstallProgress& p);

    void setHidden(const QString& archiveRelPath, bool hidden);
    void removeByKey(const QString& key);
    // Drop the transient row created by an early DownloadProgress event when its archive row arrives.
    void removeTransientByDownloadId(const QString& downloadId);

    DownloadRowData rowAt(int row) const;
    int rowForKey(const QString& key) const;
    int rowForDownloadId(const QString& downloadId) const;

    bool showHidden() const { return m_showHidden; }
    void setShowHidden(bool show);

    // Visibility predicate used by the proxy filter.
    bool rowMatchesFilter(int sourceRow) const;

private:
    std::vector<DownloadRowData> m_rows;
    QHash<QString, int> m_indexByKey;
    bool m_showHidden = false;

    void rebuildIndex();
    void emitRowChanged(int row, const QVector<int>& roles = {});
    static QString phaseLabel(DownloadPhase p);
    static QString categoryDisplay(const QString& cat);
    static QString formatSize(qint64 bytes);
    static DownloadPhase phaseFromDownloadStatus(int status);
};

} // namespace gorganizer

Q_DECLARE_METATYPE(gorganizer::DownloadRowData)
