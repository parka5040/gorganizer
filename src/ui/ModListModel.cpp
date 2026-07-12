#include "ModListModel.h"
#include "ModCatalog.h"

#include <QFont>
#include <algorithm>

namespace gorganizer {

// Builds the ModListRow for the pinned Overwrite pseudo-row.
static ModListRow makeOverwriteRow()
{
    ModListRow r;
    r.kind = RowKindOverwrite;
    r.folder = QString(kOverwriteModName);
    return r;
}

// Returns the display text the given column shows for the given row.
static QString displayText(const ModListRow& r, int column)
{
    switch (column) {
        case ModColPriority:
            if (r.kind == RowKindMod) return QString::number(r.priority);
            if (r.kind == RowKindSeparator) return r.collapsed ? QStringLiteral("▶") : QStringLiteral("▼");
            return QStringLiteral("— Overwrite —");
        case ModColConflicts:
            return r.kind == RowKindMod ? r.conflictMark : QString();
        case ModColName:
            return r.kind == RowKindOverwrite ? QString() : r.name;
        case ModColCategory:
            return r.kind == RowKindMod ? r.category : QString();
        case ModColVersion:
            return r.kind == RowKindMod ? r.version : QString();
        default:
            return {};
    }
}

// Builds the conflict-column tooltip from the stored win/loss counts.
static QString conflictTooltip(const ModListRow& r)
{
    if (r.conflictWins > 0 && r.conflictLosses > 0)
        return QString("Overwrites %1 file(s), overwritten in %2 file(s) "
                       "— right-click for details").arg(r.conflictWins).arg(r.conflictLosses);
    if (r.conflictWins > 0)
        return QString("Overwrites %1 file(s) — right-click for details").arg(r.conflictWins);
    if (r.conflictLosses > 0)
        return QString("Overwritten in %1 file(s) — right-click for details").arg(r.conflictLosses);
    return {};
}

ModListModel::ModListModel(QObject* parent)
    : QAbstractTableModel(parent)
{
}

int ModListModel::rowCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : static_cast<int>(m_rows.size());
}

int ModListModel::columnCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : ModColCount;
}

QVariant ModListModel::headerData(int section, Qt::Orientation orientation, int role) const
{
    if (role != Qt::DisplayRole || orientation != Qt::Horizontal)
        return {};
    switch (section) {
        case ModColPriority: return "Priority";
        case ModColConflicts: return "Conflicts";
        case ModColName: return "Mod Name";
        case ModColCategory: return "Category";
        case ModColVersion: return "Version";
        default: return {};
    }
}

QVariant ModListModel::data(const QModelIndex& idx, int role) const
{
    if (!idx.isValid() || idx.row() < 0 || idx.row() >= static_cast<int>(m_rows.size()))
        return {};
    const ModListRow& r = m_rows[idx.row()];

    switch (role) {
        case RowKindRole:
            return static_cast<int>(r.kind);
        case ModFolderRole:
            return r.folder;
        case ModIndexRole:
            return r.kind == RowKindMod ? r.modIndex : -1;
        case SeparatorNameRole:
            return r.kind == RowKindSeparator ? r.name : QString();
        case PriorityRole:
            return r.priority;
        case ConflictMarkRole:
            return r.conflictMark;
        case TintRole:
            return r.tint;
        case Qt::DisplayRole:
            return displayText(r, idx.column());
        case Qt::CheckStateRole:
            if (idx.column() == ModColName && r.kind == RowKindMod)
                return r.checked ? Qt::Checked : Qt::Unchecked;
            return {};
        case Qt::TextAlignmentRole:
            if (idx.column() == ModColPriority || idx.column() == ModColConflicts)
                return static_cast<int>(Qt::AlignCenter);
            return {};
        case Qt::FontRole:
            if (r.kind == RowKindSeparator && idx.column() == ModColName) {
                QFont f;
                f.setBold(true);
                f.setItalic(true);
                return f;
            }
            if (r.kind == RowKindOverwrite && idx.column() == ModColPriority) {
                QFont f;
                f.setItalic(true);
                return f;
            }
            return {};
        case Qt::ToolTipRole:
            if (r.kind == RowKindOverwrite && idx.column() == ModColPriority)
                return QStringLiteral(
                    "Always-on write-capture layer.\n"
                    "Loose .esp/.dds/.bsa files dropped here are visible in-game at the\n"
                    "highest priority. Right-click to extract into a real mod folder.");
            if (r.kind == RowKindMod && idx.column() == ModColConflicts)
                return conflictTooltip(r);
            return {};
        default:
            return {};
    }
}

bool ModListModel::setData(const QModelIndex& idx, const QVariant& value, int role)
{
    if (!idx.isValid() || idx.row() < 0 || idx.row() >= static_cast<int>(m_rows.size()))
        return false;
    if (role != Qt::CheckStateRole || idx.column() != ModColName)
        return false;
    ModListRow& r = m_rows[idx.row()];
    if (r.kind != RowKindMod)
        return false;
    bool checked = (value.toInt() == Qt::Checked);
    if (r.checked == checked)
        return true;
    r.checked = checked;
    emit dataChanged(idx, idx, {Qt::CheckStateRole});
    return true;
}

Qt::ItemFlags ModListModel::flags(const QModelIndex& idx) const
{
    if (!idx.isValid())
        return Qt::ItemIsDropEnabled;
    if (idx.row() < 0 || idx.row() >= static_cast<int>(m_rows.size()))
        return Qt::NoItemFlags;
    const ModListRow& r = m_rows[idx.row()];
    if (r.kind == RowKindOverwrite)
        return Qt::ItemIsEnabled;
    Qt::ItemFlags f = Qt::ItemIsEnabled | Qt::ItemIsSelectable
                    | Qt::ItemIsDragEnabled | Qt::ItemIsDropEnabled;
    if (r.kind == RowKindMod && idx.column() == ModColName)
        f |= Qt::ItemIsUserCheckable;
    return f;
}

Qt::DropActions ModListModel::supportedDropActions() const
{
    return Qt::MoveAction | Qt::CopyAction;
}

void ModListModel::setRows(std::vector<ModListRow> rows)
{
    beginResetModel();
    m_rows = std::move(rows);
    m_rows.push_back(makeOverwriteRow());
    assignPriorities();
    endResetModel();
}

void ModListModel::clear()
{
    beginResetModel();
    m_rows.clear();
    endResetModel();
}

const ModListRow& ModListModel::rowAt(int row) const
{
    static const ModListRow kEmpty;
    if (row < 0 || row >= static_cast<int>(m_rows.size()))
        return kEmpty;
    return m_rows[row];
}

int ModListModel::overwriteRow() const
{
    for (int r = static_cast<int>(m_rows.size()) - 1; r >= 0; --r) {
        if (m_rows[r].kind == RowKindOverwrite)
            return r;
    }
    return -1;
}

int ModListModel::rowForModIndex(int modIndex) const
{
    for (int r = 0; r < static_cast<int>(m_rows.size()); ++r) {
        if (m_rows[r].kind == RowKindMod && m_rows[r].modIndex == modIndex)
            return r;
    }
    return -1;
}

int ModListModel::rowForSeparatorName(const QString& name) const
{
    for (int r = 0; r < static_cast<int>(m_rows.size()); ++r) {
        if (m_rows[r].kind == RowKindSeparator && m_rows[r].name == name)
            return r;
    }
    return -1;
}

void ModListModel::assignPriorities()
{
    int p = 0;
    for (auto& r : m_rows) {
        if (r.kind == RowKindMod)
            r.priority = p++;
        else
            r.priority = -1;
    }
}

void ModListModel::recalculatePriorities()
{
    assignPriorities();
    if (!m_rows.empty())
        emit dataChanged(index(0, ModColPriority),
                         index(static_cast<int>(m_rows.size()) - 1, ModColPriority),
                         {Qt::DisplayRole, PriorityRole});
}

void ModListModel::permuteRows(const std::vector<int>& newOrder)
{
    std::vector<ModListRow> next;
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

void ModListModel::sortBy(int column, Qt::SortOrder order)
{
    const int count = static_cast<int>(m_rows.size());
    if (count == 0)
        return;

    std::vector<int> movable;
    std::vector<int> pinned;
    movable.reserve(count);
    for (int r = 0; r < count; ++r)
        (m_rows[r].kind == RowKindOverwrite ? pinned : movable).push_back(r);

    if (column == ModColPriority) {
        std::stable_sort(movable.begin(), movable.end(), [this, order](int a, int b) {
            return order == Qt::AscendingOrder ? m_rows[a].priority < m_rows[b].priority
                                               : m_rows[a].priority > m_rows[b].priority;
        });
    } else {
        std::stable_sort(movable.begin(), movable.end(), [this, order, column](int a, int b) {
            int cmp = displayText(m_rows[a], column)
                          .compare(displayText(m_rows[b], column), Qt::CaseInsensitive);
            return order == Qt::AscendingOrder ? cmp < 0 : cmp > 0;
        });
    }

    std::vector<int> newOrder = std::move(movable);
    newOrder.insert(newOrder.end(), pinned.begin(), pinned.end());

    emit layoutAboutToBeChanged({}, QAbstractItemModel::VerticalSortHint);
    permuteRows(newOrder);
    emit layoutChanged({}, QAbstractItemModel::VerticalSortHint);
}

void ModListModel::restorePriorityOrder()
{
    sortBy(ModColPriority, Qt::AscendingOrder);
}

int ModListModel::moveRowsTo(QList<int> srcRows, int destRow)
{
    const int count = static_cast<int>(m_rows.size());
    if (srcRows.isEmpty())
        return -1;
    std::sort(srcRows.begin(), srcRows.end());
    for (int r : srcRows) {
        if (r < 0 || r >= count || m_rows[r].kind == RowKindOverwrite)
            return -1;
    }

    int landing = destRow;
    for (int r : srcRows) {
        if (r < destRow)
            --landing;
    }

    std::vector<char> isSrc(count, 0);
    for (int r : srcRows)
        isSrc[r] = 1;

    std::vector<int> rest;
    rest.reserve(count - srcRows.size());
    for (int r = 0; r < count; ++r) {
        if (!isSrc[r])
            rest.push_back(r);
    }

    if (landing < 0)
        landing = 0;
    if (landing > static_cast<int>(rest.size()))
        landing = static_cast<int>(rest.size());

    std::vector<int> newOrder;
    newOrder.reserve(count);
    for (int i = 0; i < landing; ++i)
        newOrder.push_back(rest[i]);
    for (int r : srcRows)
        newOrder.push_back(r);
    for (int i = landing; i < static_cast<int>(rest.size()); ++i)
        newOrder.push_back(rest[i]);

    emit layoutAboutToBeChanged({}, QAbstractItemModel::VerticalSortHint);
    permuteRows(newOrder);
    emit layoutChanged({}, QAbstractItemModel::VerticalSortHint);
    return landing;
}

void ModListModel::emitAllRowsChanged(const QList<int>& roles)
{
    if (m_rows.empty())
        return;
    emit dataChanged(index(0, 0),
                     index(static_cast<int>(m_rows.size()) - 1, ModColCount - 1), roles);
}

void ModListModel::applyConflictCounts(const QHash<QString, int>& wins,
                                       const QHash<QString, int>& losses)
{
    for (auto& r : m_rows) {
        if (r.kind != RowKindMod) {
            r.conflictWins = 0;
            r.conflictLosses = 0;
            r.conflictMark.clear();
            continue;
        }
        r.conflictWins = wins.value(r.name, 0);
        r.conflictLosses = losses.value(r.name, 0);
        if (r.conflictWins > 0 && r.conflictLosses > 0)
            r.conflictMark = QStringLiteral("+-");
        else if (r.conflictWins > 0)
            r.conflictMark = QStringLiteral("+");
        else if (r.conflictLosses > 0)
            r.conflictMark = QStringLiteral("-");
        else
            r.conflictMark.clear();
    }
    if (!m_rows.empty())
        emit dataChanged(index(0, ModColConflicts),
                         index(static_cast<int>(m_rows.size()) - 1, ModColConflicts),
                         {Qt::DisplayRole, Qt::ToolTipRole, ConflictMarkRole});
}

void ModListModel::applySelectionTints(int selectedRow, const QSet<QString>& losers,
                                       const QSet<QString>& winners)
{
    for (int r = 0; r < static_cast<int>(m_rows.size()); ++r) {
        auto& row = m_rows[r];
        row.tint = TintNone;
        if (r == selectedRow || row.kind != RowKindMod)
            continue;
        if (losers.contains(row.name))
            row.tint = TintLosesToSelection;
        else if (winners.contains(row.name))
            row.tint = TintBeatsSelection;
    }
    emitAllRowsChanged({TintRole, Qt::BackgroundRole});
}

void ModListModel::clearTints()
{
    for (auto& r : m_rows)
        r.tint = TintNone;
    emitAllRowsChanged({TintRole, Qt::BackgroundRole});
}

void ModListModel::setCategoryAt(int row, const QString& category)
{
    if (row < 0 || row >= static_cast<int>(m_rows.size()))
        return;
    m_rows[row].category = category;
    emit dataChanged(index(row, ModColCategory), index(row, ModColCategory), {Qt::DisplayRole});
}

}
