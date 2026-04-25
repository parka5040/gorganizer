#include "DownloadsModel.h"

#include <QColor>
#include <QDateTime>
#include <QLocale>
#include <QVariant>

namespace gorganizer {

static QString keyForTransient(const QString& downloadId) { return "dl:" + downloadId; }
static QString keyForArchive(const QString& archiveRelPath) { return archiveRelPath; }

DownloadsModel::DownloadsModel(QObject* parent)
    : QAbstractTableModel(parent)
{
}

int DownloadsModel::rowCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : static_cast<int>(m_rows.size());
}

int DownloadsModel::columnCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : ColCount;
}

QVariant DownloadsModel::headerData(int section, Qt::Orientation orientation, int role) const
{
    if (role != Qt::DisplayRole || orientation != Qt::Horizontal)
        return {};
    switch (section) {
        case ColName: return "Mod";
        case ColVersion: return "Version";
        case ColCategory: return "Category";
        case ColStatus: return "Status";
        case ColSize: return "Size";
        case ColDownloaded: return "Downloaded";
        default: return {};
    }
}

QVariant DownloadsModel::data(const QModelIndex& idx, int role) const
{
    if (!idx.isValid() || idx.row() < 0 || idx.row() >= static_cast<int>(m_rows.size()))
        return {};
    const auto& r = m_rows[idx.row()];

    switch (role) {
        case KeyRole:
            return r.key;
        case PhaseRole:
            return static_cast<int>(r.phase);
        case ProgressRole:
            return r.pct;
        case HiddenRole:
            return r.hidden;
        case MergedRole:
            return r.merged;
        case RowDataRole:
            return QVariant::fromValue(r);
        case Qt::ForegroundRole:
            if (r.hidden)
                return QColor(140, 140, 140);
            if (r.phase == DownloadPhase::Failed)
                return QColor(200, 70, 70);
            if (r.phase == DownloadPhase::Uninstalled)
                return QColor(160, 160, 160);
            return {};
        case Qt::ToolTipRole:
            if (r.phase == DownloadPhase::Failed && !r.error.isEmpty())
                return r.error;
            if (r.phase == DownloadPhase::Installing && !r.currentFile.isEmpty())
                return r.currentFile;
            return {};
        default:
            break;
    }

    if (role != Qt::DisplayRole && role != Qt::EditRole)
        return {};

    switch (idx.column()) {
        case ColName: {
            if (!r.fileName.isEmpty() && !r.name.isEmpty() && r.fileName != r.name)
                return QString("%1 — %2").arg(r.name, r.fileName);
            return r.name.isEmpty() ? r.fileArchiveName : r.name;
        }
        case ColVersion:
            return r.version;
        case ColCategory:
            return categoryDisplay(r.category);
        case ColStatus:
            // Merged installs show "Merged" instead of "Installed" so the user
            // can tell at a glance which archives stand alone vs. were folded
            // into another mod.
            if (r.merged && r.phase == DownloadPhase::Installed)
                return QStringLiteral("Merged");
            return phaseLabel(r.phase);
        case ColSize:
            if (r.phase == DownloadPhase::Downloading && r.sizeBytes > 0)
                return QString("%1 / %2")
                    .arg(formatSize(r.bytesDownloaded), formatSize(r.sizeBytes));
            return formatSize(r.sizeBytes);
        case ColDownloaded: {
            if (r.downloadedAt.isEmpty())
                return QString("—");
            return QDateTime::fromString(r.downloadedAt, Qt::ISODate)
                .toLocalTime().toString("yyyy-MM-dd HH:mm");
        }
        default:
            return {};
    }
}

void DownloadsModel::emitRowChanged(int row, const QVector<int>& roles)
{
    if (row < 0 || row >= static_cast<int>(m_rows.size()))
        return;
    emit dataChanged(index(row, 0), index(row, ColCount - 1), roles);
}

void DownloadsModel::rebuildIndex()
{
    m_indexByKey.clear();
    m_indexByKey.reserve(static_cast<int>(m_rows.size()));
    for (int i = 0; i < static_cast<int>(m_rows.size()); ++i)
        m_indexByKey.insert(m_rows[i].key, i);
}

DownloadRowData DownloadsModel::rowAt(int row) const
{
    if (row < 0 || row >= static_cast<int>(m_rows.size()))
        return {};
    return m_rows[row];
}

int DownloadsModel::rowForKey(const QString& key) const
{
    return m_indexByKey.value(key, -1);
}

int DownloadsModel::rowForDownloadId(const QString& downloadId) const
{
    if (downloadId.isEmpty())
        return -1;
    for (int i = 0; i < static_cast<int>(m_rows.size()); ++i) {
        if (m_rows[i].downloadId == downloadId)
            return i;
    }
    return -1;
}

void DownloadsModel::setShowHidden(bool show)
{
    if (m_showHidden == show)
        return;
    m_showHidden = show;
    // The proxy filter re-invalidates on its own when the view asks it to;
    // we just flip the flag. Views should call invalidate on their proxy.
}

bool DownloadsModel::rowMatchesFilter(int sourceRow) const
{
    if (sourceRow < 0 || sourceRow >= static_cast<int>(m_rows.size()))
        return false;
    if (m_showHidden)
        return true;
    return !m_rows[sourceRow].hidden;
}

// Status mapping: proto DownloadStatus is the SINGLE source of truth shared by
// streaming progress and snapshot list rows. Values match the unified proto
// numerically (0=unknown, 1=queued, 2=downloading, 3=downloaded, 4=installing,
// 5=installed, 6=uninstalled, 7=cancelled, 8=failed). This is the only place
// that maps the wire int to a phase.
DownloadPhase DownloadsModel::phaseFromDownloadStatus(int status)
{
    switch (status) {
        case 1: return DownloadPhase::Queued;
        case 2: return DownloadPhase::Downloading;
        case 3: return DownloadPhase::Downloaded;
        case 4: return DownloadPhase::Installing;
        case 5: return DownloadPhase::Installed;
        case 6: return DownloadPhase::Uninstalled;
        case 7: return DownloadPhase::Cancelled;
        case 8: return DownloadPhase::Failed;
        default: return DownloadPhase::Unknown;
    }
}

QString DownloadsModel::phaseLabel(DownloadPhase p)
{
    switch (p) {
        case DownloadPhase::Queued:      return "Queued";
        case DownloadPhase::Downloading: return "Downloading";
        case DownloadPhase::Downloaded:  return "Downloaded";
        case DownloadPhase::Installing:  return "Installing";
        case DownloadPhase::Installed:   return "Installed";
        case DownloadPhase::Uninstalled: return "Uninstalled";
        case DownloadPhase::Cancelled:   return "Cancelled";
        case DownloadPhase::Failed:      return "Failed";
        default: return "—";
    }
}

QString DownloadsModel::categoryDisplay(const QString& cat)
{
    if (cat.isEmpty())
        return "—";
    QString c = cat;
    c[0] = c[0].toUpper();
    c.replace('_', ' ');
    return c;
}

QString DownloadsModel::formatSize(qint64 bytes)
{
    if (bytes <= 0)
        return "—";
    return QLocale().formattedDataSize(bytes);
}

void DownloadsModel::replaceFromDaemon(const std::vector<GrpcDownloadRow>& rows)
{
    // Bucket existing transient rows (keyed by dl:<id>) by their downloadId.
    // The daemon echoes download_id on archive rows that correspond to an
    // in-flight download, so we can promote the transient row into the
    // archive row in place — no duplicate "Waiting + Completed" pair.
    QHash<QString, DownloadRowData> transientById;
    for (const auto& r : m_rows) {
        if (!r.downloadId.isEmpty() && r.archiveRelPath.isEmpty())
            transientById.insert(r.downloadId, r);
    }

    beginResetModel();
    m_rows.clear();
    m_rows.reserve(rows.size() + transientById.size());
    for (const auto& src : rows) {
        DownloadRowData r;
        r.key = keyForArchive(src.archiveRelPath);
        r.archiveRelPath = src.archiveRelPath;
        r.name = src.modName;
        r.fileName = src.fileName;
        r.fileArchiveName = src.fileArchiveName;
        r.modId = src.modId;
        r.fileId = src.fileId;
        r.version = src.version;
        r.category = src.category;
        r.sizeBytes = src.sizeBytes;
        r.bytesDownloaded = src.sizeBytes; // on-disk already
        r.uploadedAt = src.uploadedAt;
        r.downloadedAt = src.downloadedAt;
        r.gameDomain = src.gameDomain;
        r.thumbnailUrl = src.thumbnailUrl;
        r.adultContent = src.adultContent;
        r.hidden = src.hidden;
        r.installedModFolder = src.installedModFolder;
        r.merged = src.merged;
        r.pct = -1;
        r.phase = phaseFromDownloadStatus(src.status);

        // Promotion: if the daemon attached a downloadId, consume the matching
        // transient (keep its live bytes/pct/error) and drop it from the
        // carry-forward map so it doesn't get re-added below.
        if (!src.downloadId.isEmpty()) {
            auto it = transientById.find(src.downloadId);
            if (it != transientById.end()) {
                r.bytesDownloaded = it->bytesDownloaded;
                r.pct = it->pct;
                if (!it->error.isEmpty())
                    r.error = it->error;
                r.downloadId = src.downloadId;
                transientById.erase(it);
            }
        }
        m_rows.push_back(std::move(r));
    }
    // Carry forward any transients whose downloadId did NOT appear in the
    // daemon's snapshot — the archive hasn't landed yet (first few ticks).
    for (auto it = transientById.begin(); it != transientById.end(); ++it)
        m_rows.push_back(*it);
    rebuildIndex();
    endResetModel();
}

void DownloadsModel::applyDownloadProgress(const GrpcDownloadProgress& p)
{
    // Find the matching row by downloadId. Create a transient if none exists
    // yet (happens during the first few progress ticks before ListDownloads
    // has the archive entry).
    int row = rowForDownloadId(p.downloadId);
    if (row < 0) {
        DownloadRowData r;
        r.key = keyForTransient(p.downloadId);
        r.downloadId = p.downloadId;
        r.name = p.modName.isEmpty() ? p.downloadId : p.modName;
        r.bytesDownloaded = p.bytesDownloaded;
        r.sizeBytes = p.bytesTotal;
        r.phase = phaseFromDownloadStatus(p.status);
        r.pct = (p.bytesTotal > 0)
            ? static_cast<int>(p.bytesDownloaded * 100 / p.bytesTotal)
            : -1;
        r.error = p.error;

        beginInsertRows({}, static_cast<int>(m_rows.size()), static_cast<int>(m_rows.size()));
        m_indexByKey.insert(r.key, static_cast<int>(m_rows.size()));
        m_rows.push_back(std::move(r));
        endInsertRows();
        return;
    }
    auto& r = m_rows[row];
    r.name = p.modName.isEmpty() ? r.name : p.modName;
    r.bytesDownloaded = p.bytesDownloaded;
    if (p.bytesTotal > 0)
        r.sizeBytes = p.bytesTotal;
    r.phase = phaseFromDownloadStatus(p.status);
    // Unified proto DownloadStatus: DOWNLOADED=3 and INSTALLED=5 are terminal
    // for the bytes channel; pin pct=100 so the row's progress bar reads full
    // instead of indeterminate when bytes_total wasn't advertised.
    bool terminal = (p.status == 3 || p.status == 5);
    r.pct = (p.bytesTotal > 0)
        ? static_cast<int>(p.bytesDownloaded * 100 / p.bytesTotal)
        : (terminal ? 100 : -1);
    r.error = p.error;
    emitRowChanged(row, {PhaseRole, ProgressRole, Qt::DisplayRole, Qt::ToolTipRole, Qt::ForegroundRole});
}

void DownloadsModel::applyInstallProgress(const GrpcInstallProgress& p)
{
    if (p.archiveRelPath.isEmpty())
        return;
    int row = rowForKey(keyForArchive(p.archiveRelPath));
    if (row < 0)
        return;  // row will appear on the next ListDownloads; install events before then are dropped
    auto& r = m_rows[row];
    switch (p.step) {
        case GrpcInstallStepExtracting:
        case GrpcInstallStepCopying:
        case GrpcInstallStepFinalizing:
            r.phase = DownloadPhase::Installing;
            break;
        case GrpcInstallStepComplete:
            r.phase = DownloadPhase::Installed;
            break;
        case GrpcInstallStepFailed:
            r.phase = DownloadPhase::Failed;
            break;
        default:
            break;
    }
    r.pct = p.pct;
    r.currentFile = p.currentFile;
    r.filesDone = p.filesDone;
    r.filesTotal = p.filesTotal;
    if (!p.error.isEmpty())
        r.error = p.error;
    if (!p.modName.isEmpty())
        r.name = p.modName;
    emitRowChanged(row, {PhaseRole, ProgressRole, Qt::DisplayRole, Qt::ToolTipRole, Qt::ForegroundRole});
}

void DownloadsModel::setHidden(const QString& archiveRelPath, bool hidden)
{
    int row = rowForKey(keyForArchive(archiveRelPath));
    if (row < 0)
        return;
    m_rows[row].hidden = hidden;
    emitRowChanged(row, {HiddenRole, Qt::ForegroundRole});
}

void DownloadsModel::removeByKey(const QString& key)
{
    int row = rowForKey(key);
    if (row < 0)
        return;
    beginRemoveRows({}, row, row);
    m_rows.erase(m_rows.begin() + row);
    rebuildIndex();
    endRemoveRows();
}

void DownloadsModel::removeTransientByDownloadId(const QString& downloadId)
{
    if (downloadId.isEmpty())
        return;
    for (int i = 0; i < static_cast<int>(m_rows.size()); ++i) {
        if (m_rows[i].downloadId != downloadId)
            continue;
        // Only remove rows that are still in transient form (no archive
        // path yet) — a fully promoted row that happens to share a
        // download_id should not be wiped.
        if (!m_rows[i].archiveRelPath.isEmpty())
            return;
        beginRemoveRows({}, i, i);
        m_rows.erase(m_rows.begin() + i);
        rebuildIndex();
        endRemoveRows();
        return;
    }
}

} // namespace gorganizer
