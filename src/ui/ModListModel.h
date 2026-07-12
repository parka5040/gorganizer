#pragma once

#include <QAbstractTableModel>
#include <QHash>
#include <QList>
#include <QSet>
#include <QString>
#include <vector>

namespace gorganizer {

enum ModColumn { ModColPriority = 0, ModColConflicts = 1, ModColName = 2, ModColCategory = 3, ModColVersion = 4, ModColCount = 5 };

enum ModRowKind { RowKindMod = 0, RowKindSeparator = 1, RowKindOverwrite = 2 };

struct ModListRow {
    ModRowKind kind = RowKindMod;
    int modIndex = -1;
    QString folder;
    QString name;
    QString category;
    QString version;
    bool checked = false;
    bool collapsed = false;
    int priority = -1;
    QString conflictMark;
    int conflictWins = 0;
    int conflictLosses = 0;
    int tint = 0;
};

class ModListModel : public QAbstractTableModel {
    Q_OBJECT
public:
    enum Roles {
        RowKindRole = Qt::UserRole + 1,
        ModFolderRole,
        ModIndexRole,
        SeparatorNameRole,
        PriorityRole,
        ConflictMarkRole,
        TintRole,
    };

    enum Tint { TintNone = 0, TintLosesToSelection = 1, TintBeatsSelection = 2 };

    explicit ModListModel(QObject* parent = nullptr);

    int rowCount(const QModelIndex& parent = {}) const override;
    int columnCount(const QModelIndex& parent = {}) const override;
    QVariant data(const QModelIndex& idx, int role) const override;
    bool setData(const QModelIndex& idx, const QVariant& value, int role) override;
    QVariant headerData(int section, Qt::Orientation orientation, int role) const override;
    Qt::ItemFlags flags(const QModelIndex& idx) const override;
    Qt::DropActions supportedDropActions() const override;

    // Replaces all rows, appends the pinned Overwrite pseudo-row, and assigns priorities.
    void setRows(std::vector<ModListRow> rows);
    void clear();

    const ModListRow& rowAt(int row) const;
    // Locates the pinned Overwrite row by its kind tag rather than by row position.
    int overwriteRow() const;
    int rowForModIndex(int modIndex) const;
    int rowForSeparatorName(const QString& name) const;

    void recalculatePriorities();
    // Sorts all non-Overwrite rows in place by the given column; the Overwrite row stays pinned last.
    void sortBy(int column, Qt::SortOrder order);
    void restorePriorityOrder();
    // Moves srcRows (Overwrite excluded) so the block lands at destRow; returns the landing row or -1.
    int moveRowsTo(QList<int> srcRows, int destRow);

    void applyConflictCounts(const QHash<QString, int>& wins, const QHash<QString, int>& losses);
    // Tints mod rows that lose to / beat the single-selected row; the selected row itself is untinted.
    void applySelectionTints(int selectedRow, const QSet<QString>& losers, const QSet<QString>& winners);
    void clearTints();
    void setCategoryAt(int row, const QString& category);

private:
    std::vector<ModListRow> m_rows;

    void assignPriorities();
    void permuteRows(const std::vector<int>& newOrder);
    void emitAllRowsChanged(const QList<int>& roles);
};

}
