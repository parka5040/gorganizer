#pragma once

#include <QAbstractTableModel>
#include <QHash>
#include <QString>
#include <vector>
#include "GrpcTypes.h"

namespace gorganizer {

enum SmapiComponentColumn {
    SmapiColName = 0,
    SmapiColUniqueId = 1,
    SmapiColVersion = 2,
    SmapiColType = 3,
    SmapiColStatus = 4,
    SmapiColProvider = 5,
    SmapiColUpdate = 6,
    SmapiColCount = 7,
};

class SmapiComponentModel : public QAbstractTableModel {
    Q_OBJECT
public:
    enum Roles {
        UniqueIdRole = Qt::UserRole + 1,
        ProviderModRole,
        SeverityRole,
        UpdateVersionRole,
        UpdateUrlRole,
        BundledRole,
        FailedRole,
    };

    explicit SmapiComponentModel(QObject* parent = nullptr);

    int rowCount(const QModelIndex& parent = {}) const override;
    int columnCount(const QModelIndex& parent = {}) const override;
    QVariant data(const QModelIndex& idx, int role) const override;
    QVariant headerData(int section, Qt::Orientation orientation, int role) const override;

    // Replaces the rows with a report's components, problems first, then by name with bundled mods last.
    void setReport(const GrpcModDependencyReport& report);
    void clear();

private:
    // Returns the Status column tooltip listing every issue of a component.
    QString statusTooltip(const GrpcModComponent& component) const;

    std::vector<GrpcModComponent> m_components;
    QHash<QString, QString> m_names;
};

}
