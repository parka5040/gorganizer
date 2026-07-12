#include "PluginListModel.h"
#include "GameInfo.h"
#include "PluginScanner.h"

#include <QFont>
#include <QObject>
#include <algorithm>
#include <numeric>

namespace gorganizer {

// Returns the display label for a plugin type.
static QString typeString(int type)
{
    switch (type) {
    case PluginEntry::ESM: return "ESM";
    case PluginEntry::ESL: return "ESL";
    case PluginEntry::ESP: return "ESP";
    default: return "???";
    }
}

// Severity rank of a dep-issue kind; higher wins when picking a row's worst issue.
static int rankKind(int kind)
{
    switch (kind) {
    case GrpcDepMasterAbsent:
    case GrpcDepMasterOutOfOrder:
        return 3;
    case GrpcDepMasterDisabled:
        return 2;
    case GrpcDepSoftMissing:
        return 1;
    default:
        return 0;
    }
}

// Human-readable one-line description of a dep issue.
static QString humanIssue(const GrpcDepIssue& iss)
{
    switch (iss.kind) {
    case GrpcDepMasterAbsent:
        return QObject::tr("Missing master: %1").arg(iss.master);
    case GrpcDepMasterDisabled:
        return QObject::tr("Master disabled: %1").arg(iss.master);
    case GrpcDepMasterOutOfOrder:
        return QObject::tr("Master loads after dependent: %1").arg(iss.master);
    case GrpcDepSoftMissing:
        if (!iss.softModName.isEmpty())
            return QObject::tr("Soft dependency missing: %1").arg(iss.softModName);
        return QObject::tr("Soft dependency missing");
    default:
        return QString();
    }
}

// True when the filename is one of the game's engine-pinned master files.
static bool isGameMaster(const QString& filename, const QString& gameShortName)
{
    const QStringList masters = GameInfo::mastersFor(gameShortName);
    for (const auto& master : masters) {
        if (filename.compare(master, Qt::CaseInsensitive) == 0)
            return true;
    }
    return false;
}

// Returns the display text the given column shows for the given row.
static QString displayText(const PluginListRow& r, int column)
{
    switch (column) {
    case PluginColIndex: return r.indexLabel;
    case PluginColName: return r.filename;
    case PluginColType: return typeString(r.type);
    default: return {};
    }
}

PluginListModel::PluginListModel(QObject* parent)
    : QAbstractTableModel(parent)
{
}

int PluginListModel::rowCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : static_cast<int>(m_rows.size());
}

int PluginListModel::columnCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : PluginColCount;
}

QVariant PluginListModel::headerData(int section, Qt::Orientation orientation, int role) const
{
    if (role != Qt::DisplayRole || orientation != Qt::Horizontal)
        return {};
    switch (section) {
    case PluginColIndex: return "Index";
    case PluginColName: return "Plugin";
    case PluginColType: return "Type";
    default: return {};
    }
}

QVariant PluginListModel::data(const QModelIndex& idx, int role) const
{
    if (!idx.isValid() || idx.row() < 0 || idx.row() >= static_cast<int>(m_rows.size()))
        return {};
    const PluginListRow& r = m_rows[idx.row()];

    switch (role) {
    case PluginNameRole:
        return r.filename;
    case PluginTypeRole:
        return r.type;
    case PinnedRole:
        return r.pinned;
    case LoadOrderRole:
        return r.loadOrder;
    case DepWorstKindRole:
        return r.depWorstKind;
    case DepIssuesRole:
        return r.depIssues;
    case SoftPendingRole:
        return r.softPending;
    case Qt::DisplayRole:
        return displayText(r, idx.column());
    case Qt::CheckStateRole:
        if (idx.column() == PluginColName)
            return r.checked ? Qt::Checked : Qt::Unchecked;
        return {};
    case Qt::TextAlignmentRole:
        if (idx.column() != PluginColName)
            return static_cast<int>(Qt::AlignCenter);
        return {};
    case Qt::FontRole:
        if (idx.column() == PluginColName && r.pinned) {
            QFont f;
            f.setItalic(true);
            return f;
        }
        return {};
    case Qt::ToolTipRole:
        if (idx.column() == PluginColStatus)
            return r.depIssues.join("\n");
        return {};
    default:
        return {};
    }
}

bool PluginListModel::setData(const QModelIndex& idx, const QVariant& value, int role)
{
    if (!idx.isValid() || idx.row() < 0 || idx.row() >= static_cast<int>(m_rows.size()))
        return false;
    if (role != Qt::CheckStateRole || idx.column() != PluginColName)
        return false;
    PluginListRow& r = m_rows[idx.row()];
    bool checked = (value.toInt() == Qt::Checked);
    if (r.checked == checked)
        return true;
    r.checked = checked;
    emit dataChanged(idx, idx, {Qt::CheckStateRole});
    emit activationEdited();
    return true;
}

Qt::ItemFlags PluginListModel::flags(const QModelIndex& idx) const
{
    if (!idx.isValid())
        return Qt::ItemIsDropEnabled;
    if (idx.row() < 0 || idx.row() >= static_cast<int>(m_rows.size()))
        return Qt::NoItemFlags;
    const PluginListRow& r = m_rows[idx.row()];
    Qt::ItemFlags f = Qt::ItemIsSelectable | Qt::ItemIsDropEnabled;
    if (!(r.pinned && idx.column() == PluginColName))
        f |= Qt::ItemIsEnabled;
    if (idx.column() == PluginColName)
        f |= Qt::ItemIsUserCheckable;
    if (!r.pinned && (idx.column() == PluginColName || idx.column() == PluginColType))
        f |= Qt::ItemIsDragEnabled;
    return f;
}

Qt::DropActions PluginListModel::supportedDropActions() const
{
    return Qt::MoveAction | Qt::CopyAction;
}

void PluginListModel::clear()
{
    beginResetModel();
    m_rows.clear();
    endResetModel();
}

void PluginListModel::applyStatusToRow(int row, const GrpcPluginStatus& s)
{
    if (row < 0 || row >= static_cast<int>(m_rows.size()))
        return;
    int worst = 0;
    QStringList details;
    for (const auto& iss : s.issues) {
        if (iss.kind == GrpcDepOK) continue;
        if (rankKind(iss.kind) > rankKind(worst)) worst = iss.kind;
        QString text = humanIssue(iss);
        if (!text.isEmpty()) details << text;
    }
    PluginListRow& r = m_rows[row];
	r.checked = r.pinned || s.enabled;
    r.depWorstKind = worst;
    r.depIssues = details;
    r.softPending = s.softPending;
    emit dataChanged(index(row, 0), index(row, PluginColCount - 1));
}

void PluginListModel::applySnapshot(const std::vector<GrpcPluginStatus>& plugins,
                                    const QString& gameShortName)
{
    beginResetModel();
    m_rows.clear();
    m_lastStatus.clear();
    m_preserveMixedOrder = gameShortName.compare("oblivionremastered", Qt::CaseInsensitive) == 0;
    m_rows.reserve(plugins.size());
    for (const auto& s : plugins) {
        m_lastStatus.insert(s.filename.toLower(), s);
        PluginListRow row;
        row.filename = s.filename;
        row.pinned = isGameMaster(s.filename, gameShortName);
        row.checked = row.pinned || s.enabled;
        row.loadOrder = static_cast<int>(m_rows.size());
        if (s.isLight || s.ext.compare(".esl", Qt::CaseInsensitive) == 0)
            row.type = PluginEntry::ESL;
        else if (s.ext.compare(".esm", Qt::CaseInsensitive) == 0)
            row.type = PluginEntry::ESM;
        else
            row.type = PluginEntry::ESP;
        m_rows.push_back(std::move(row));
    }
    recalculateIndices();
    endResetModel();

    for (int row = 0; row < static_cast<int>(plugins.size()); ++row)
        applyStatusToRow(row, plugins[row]);
}

void PluginListModel::applyUpdate(const GrpcPluginStatus& plugin)
{
    m_lastStatus.insert(plugin.filename.toLower(), plugin);
    for (int r = 0; r < static_cast<int>(m_rows.size()); ++r) {
        if (m_rows[r].filename.compare(plugin.filename, Qt::CaseInsensitive) == 0) {
            applyStatusToRow(r, plugin);
            return;
        }
    }
}

bool PluginListModel::canMove(int srcRow, int destRow, QString* reason) const
{
    auto setReason = [&](const QString& r) { if (reason) *reason = r; };
    const int count = static_cast<int>(m_rows.size());

    if (srcRow < 0 || srcRow >= count) {
        setReason("Nothing to move.");
        return false;
    }
    if (destRow < 0 || destRow > count) {
        setReason("Invalid drop position.");
        return false;
    }
    if (srcRow == destRow || srcRow + 1 == destRow) {
        setReason(QString());
        return false;
    }

    const PluginListRow& src = m_rows[srcRow];
    if (src.pinned) {
        setReason(QString("\"%1\" is pinned and its load order is fixed by the engine.")
                      .arg(src.filename));
        return false;
    }

    if (m_preserveMixedOrder)
        return true;

    int srcType = src.type;

    int insertAt = destRow;
    if (srcRow < destRow)
        insertAt--;

    auto typeAtEffective = [&](int effectiveRow) -> int {
        int modelRow = effectiveRow;
        if (modelRow >= srcRow)
            modelRow++;
        if (modelRow < 0 || modelRow >= count)
            return -1;
        return m_rows[modelRow].type;
    };

    auto pinnedAtEffective = [&](int effectiveRow) -> bool {
        int modelRow = effectiveRow;
        if (modelRow >= srcRow)
            modelRow++;
        if (modelRow < 0 || modelRow >= count)
            return false;
        return m_rows[modelRow].pinned;
    };

    if (insertAt < count - 1 && pinnedAtEffective(insertAt)) {
        setReason("That position is fixed by a pinned plugin above it.");
        return false;
    }

    if (insertAt > 0) {
        int aboveType = typeAtEffective(insertAt - 1);
        if (aboveType > srcType) {
            setReason("Masters (.esm) must load before regular plugins — "
                      "can't move this plugin above a master.");
            return false;
        }
    }

    if (insertAt < count - 1) {
        int belowType = typeAtEffective(insertAt);
        if (belowType >= 0 && belowType < srcType) {
            setReason("Masters (.esm) must load before regular plugins — "
                      "can't move this master below a regular plugin.");
            return false;
        }
    }

    return true;
}

int PluginListModel::moveRowTo(int srcRow, int destRow)
{
    const int count = static_cast<int>(m_rows.size());
    if (srcRow < 0 || srcRow >= count)
        return -1;

    int insertAt = destRow;
    if (srcRow < destRow)
        insertAt--;
    if (insertAt < 0)
        insertAt = 0;
    if (insertAt > count - 1)
        insertAt = count - 1;

    std::vector<int> newOrder;
    newOrder.reserve(count);
    for (int r = 0; r < count; ++r) {
        if (r != srcRow)
            newOrder.push_back(r);
    }
    newOrder.insert(newOrder.begin() + insertAt, srcRow);

    emit layoutAboutToBeChanged({}, QAbstractItemModel::VerticalSortHint);
    permuteRows(newOrder);
    emit layoutChanged({}, QAbstractItemModel::VerticalSortHint);

    for (int r = 0; r < count; ++r)
        m_rows[r].loadOrder = r;
    recalculateIndices();
    if (count > 0)
        emit dataChanged(index(0, 0), index(count - 1, PluginColCount - 1));
    return insertAt;
}

void PluginListModel::permuteRows(const std::vector<int>& newOrder)
{
    std::vector<PluginListRow> next;
    next.reserve(newOrder.size());
    for (int oldRow : newOrder)
        next.push_back(std::move(m_rows[oldRow]));

    std::vector<int> newRowOf(newOrder.size());
    for (int newRow = 0; newRow < static_cast<int>(newOrder.size()); ++newRow)
        newRowOf[newOrder[newRow]] = newRow;

    m_rows = std::move(next);

    const QModelIndexList from = persistentIndexList();
    QModelIndexList to;
    to.reserve(from.size());
    for (const QModelIndex& idx : from)
        to.append(index(newRowOf[idx.row()], idx.column()));
    changePersistentIndexList(from, to);
}

void PluginListModel::sortBy(int column, Qt::SortOrder order)
{
    const int count = static_cast<int>(m_rows.size());
    if (count == 0)
        return;

    std::vector<int> newOrder(count);
    std::iota(newOrder.begin(), newOrder.end(), 0);

    if (column == PluginColIndex) {
        std::stable_sort(newOrder.begin(), newOrder.end(), [this, order](int a, int b) {
            return order == Qt::AscendingOrder ? m_rows[a].loadOrder < m_rows[b].loadOrder
                                               : m_rows[a].loadOrder > m_rows[b].loadOrder;
        });
    } else {
        std::stable_sort(newOrder.begin(), newOrder.end(), [this, order, column](int a, int b) {
            int cmp = displayText(m_rows[a], column)
                          .compare(displayText(m_rows[b], column), Qt::CaseInsensitive);
            return order == Qt::AscendingOrder ? cmp < 0 : cmp > 0;
        });
    }

    emit layoutAboutToBeChanged({}, QAbstractItemModel::VerticalSortHint);
    permuteRows(newOrder);
    emit layoutChanged({}, QAbstractItemModel::VerticalSortHint);
}

void PluginListModel::restoreLoadOrder()
{
    sortBy(PluginColIndex, Qt::AscendingOrder);
}

void PluginListModel::revalidateOrderingIssues()
{
    if (m_lastStatus.isEmpty())
        return;

    const QHash<QString, int> rowByName = rowsByLowerName();
    for (int r = 0; r < static_cast<int>(m_rows.size()); ++r) {
        auto cachedIt = m_lastStatus.constFind(m_rows[r].filename.toLower());
        if (cachedIt == m_lastStatus.constEnd()) continue;

        GrpcPluginStatus revised = *cachedIt;
        revised.issues.clear();
        for (const auto& iss : cachedIt->issues) {
            if (iss.kind == GrpcDepMasterOutOfOrder) {
                auto mIt = rowByName.constFind(iss.master.toLower());
                if (mIt != rowByName.constEnd() && mIt.value() < r)
                    continue;
            }
            revised.issues.push_back(iss);
        }
        applyStatusToRow(r, revised);
    }
}

std::vector<GrpcPluginLoadoutEntry> PluginListModel::orderedLoadout() const
{
    std::vector<GrpcPluginLoadoutEntry> loadout;
    loadout.reserve(m_rows.size());
    for (const auto& row : m_rows) {
        if (!row.filename.isEmpty())
            loadout.push_back(GrpcPluginLoadoutEntry{row.filename, row.pinned || row.checked});
    }
    return loadout;
}

void PluginListModel::recalculateIndices()
{
    std::vector<int> order(m_rows.size());
    std::iota(order.begin(), order.end(), 0);
    std::sort(order.begin(), order.end(), [this](int a, int b) {
        return m_rows[a].loadOrder < m_rows[b].loadOrder;
    });

    int fullIndex = 0;
    int lightIndex = 0;
    for (int row : order) {
        PluginListRow& r = m_rows[row];
        if (r.type == PluginEntry::ESL) {
            r.indexLabel = QString("FE:%1").arg(lightIndex, 3, 16, QChar('0')).toUpper();
            lightIndex++;
        } else {
            r.indexLabel = QString("%1").arg(fullIndex, 2, 16, QChar('0')).toUpper();
            fullIndex++;
        }
    }
}

QHash<QString, int> PluginListModel::rowsByLowerName() const
{
    QHash<QString, int> rowByName;
    rowByName.reserve(static_cast<int>(m_rows.size()));
    for (int r = 0; r < static_cast<int>(m_rows.size()); ++r)
        rowByName.insert(m_rows[r].filename.toLower(), r);
    return rowByName;
}

}
