#pragma once

#include <QAbstractTableModel>
#include <QHash>
#include <QVector>
#include <vector>

#include "GrpcClient.h"

namespace gorganizer {

// Unified phase for a download row. Values MUST match the unified proto
// DownloadStatus enum — the frontend derives the phase directly from the
// backend status and does not invent intermediate states.
//   Queued → Downloading → Downloaded → Installing → Installed (terminal)
// Uninstalled is a sticky re-visit of Downloaded after a previous install.
// Cancelled/Failed are terminal for errors.
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
    QString key;              // stable — archive relpath OR "dl:<downloadId>" for pre-archive rows
    QString archiveRelPath;
    QString downloadId;       // set while transient; cleared once promoted
    QString name;             // display name (mod name or archive base)
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
    int pct = -1;             // 0-100 for Downloading/Installing; -1 = indeterminate
    QString currentFile;
    qint64 filesDone = 0;
    qint64 filesTotal = 0;
    QString error;
    bool hidden = false;
    QString installedModFolder;
    bool merged = false;          // archive merged into a pre-existing mod
};

// DownloadsModel is a QAbstractTableModel keyed by a stable archive
// relpath (or a transient "dl:<downloadId>" for downloads that haven't
// produced an archive yet). All updates happen in-place via
// dataChanged() — no beginResetModel, no row removal on completion.
class DownloadsModel : public QAbstractTableModel {
    Q_OBJECT
public:
    enum Column {
        ColName = 0,
        ColVersion,
        ColCategory,
        ColStatus,     // progress bar + phase chip rendered by DownloadsRowDelegate
        ColSize,
        ColDownloaded,
        ColCount,
    };

    enum Roles {
        PhaseRole = Qt::UserRole + 1,
        ProgressRole,      // int 0-100, -1 = indeterminate
        KeyRole,           // QString
        RowDataRole,       // QVariant wrapping DownloadRowData (via QMetaType)
        HiddenRole,
        MergedRole,        // bool — archive merged into a pre-existing mod
    };

    explicit DownloadsModel(QObject* parent = nullptr);

    // QAbstractTableModel overrides.
    int rowCount(const QModelIndex& parent = {}) const override;
    int columnCount(const QModelIndex& parent = {}) const override;
    QVariant data(const QModelIndex& idx, int role) const override;
    QVariant headerData(int section, Qt::Orientation orientation, int role) const override;

    // Bulk reconcile with the daemon's ListDownloads result. Preserves
    // transient in-flight rows by matching on downloadId when the matching
    // archive has since appeared in the daemon listing.
    void replaceFromDaemon(const std::vector<GrpcDownloadRow>& rows);

    // Apply live updates from the gRPC WatchStatus stream.
    void applyDownloadProgress(const GrpcDownloadProgress& p);
    void applyInstallProgress(const GrpcInstallProgress& p);

    // Instant UI response for user actions (no full reload required).
    void setHidden(const QString& archiveRelPath, bool hidden);
    void removeByKey(const QString& key);
    // Drop the transient row created by an early DownloadProgress event
    // when its real archive row arrives. Called from the RowChanged
    // handler so the post-completion `reloadFromDaemon()` snapshot
    // doesn't end up alongside the dl:<id> orphan.
    void removeTransientByDownloadId(const QString& downloadId);

    // Lookups used by the view's context menu.
    DownloadRowData rowAt(int row) const;
    int rowForKey(const QString& key) const;
    int rowForDownloadId(const QString& downloadId) const;

    bool showHidden() const { return m_showHidden; }
    void setShowHidden(bool show);

    // Filter predicate used by the view's QSortFilterProxyModel. Returns
    // true when the row at sourceRow should be visible given the current
    // showHidden toggle.
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
