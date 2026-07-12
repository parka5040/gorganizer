#pragma once

#include <QAbstractTableModel>
#include <QHash>
#include <QString>
#include <QStringList>
#include <vector>

#include "GrpcTypes.h"

namespace gorganizer {

enum PluginColumn { PluginColIndex = 0, PluginColName = 1, PluginColType = 2, PluginColStatus = 3, PluginColCount = 4 };

struct PluginListRow {
    QString filename;
    int type = 0;
    bool pinned = false;
    bool checked = true;
    int loadOrder = 0;
    QString indexLabel;
    int depWorstKind = 0;
    QStringList depIssues;
    bool softPending = false;
};

class PluginListModel : public QAbstractTableModel {
    Q_OBJECT
public:
    enum Roles {
        PluginNameRole = Qt::UserRole + 1,
        PluginTypeRole,
        PinnedRole,
        LoadOrderRole,
        DepWorstKindRole,
        DepIssuesRole,
        SoftPendingRole,
    };

    explicit PluginListModel(QObject* parent = nullptr);

    int rowCount(const QModelIndex& parent = {}) const override;
    int columnCount(const QModelIndex& parent = {}) const override;
    QVariant data(const QModelIndex& idx, int role) const override;
    bool setData(const QModelIndex& idx, const QVariant& value, int role) override;
    QVariant headerData(int section, Qt::Orientation orientation, int role) const override;
    Qt::ItemFlags flags(const QModelIndex& idx) const override;
    Qt::DropActions supportedDropActions() const override;

    void clear();

    // Replaces rows with the daemon's authoritative ordered snapshot.
    void applySnapshot(const std::vector<GrpcPluginStatus>& plugins, const QString& gameShortName);
    void applyUpdate(const GrpcPluginStatus& plugin);

    // Move legality: bounds, no-op drops (empty reason), pinned rows, and masters-before-plugins type ordering.
    bool canMove(int srcRow, int destRow, QString* reason = nullptr) const;
    // Moves srcRow so it lands at destRow, restamps load order to the new row order, and returns the landing row.
    int moveRowTo(int srcRow, int destRow);

    // Sorts rows by the given column; PluginColIndex sorts by load order, others by display text.
    void sortBy(int column, Qt::SortOrder order);
    void restoreLoadOrder();

    // Re-applies cached statuses, dropping MasterOutOfOrder issues whose master now loads above the dependent row.
    void revalidateOrderingIssues();

    std::vector<GrpcPluginLoadoutEntry> orderedLoadout() const;

signals:
    void activationEdited();

private:
    std::vector<PluginListRow> m_rows;
    QHash<QString, GrpcPluginStatus> m_lastStatus;
    bool m_preserveMixedOrder = false;

    void applyStatusToRow(int row, const GrpcPluginStatus& s);
    void recalculateIndices();
    void permuteRows(const std::vector<int>& newOrder);
    QHash<QString, int> rowsByLowerName() const;
};

}
