#pragma once

#include <QDialog>
#include <QHash>
#include <QStringList>
#include <vector>
#include "GrpcTypes.h"

class QPushButton;
class QTreeWidget;
class QTreeWidgetItem;

namespace gorganizer {

class DependencyFetchDialog : public QDialog {
    Q_OBJECT
public:
    // Lists missing dependencies to fetch; rows whose smapi.io lookup found no Nexus page cannot be picked.
    DependencyFetchDialog(const std::vector<GrpcMissingDependency>& dependencies,
                          const QHash<QString, QString>& names, const QString& profileName,
                          QWidget* parent = nullptr);

    // Returns the UniqueIDs the user left checked.
    QStringList selectedIds() const;

private slots:
    void onItemChanged(QTreeWidgetItem* item, int column);

private:
    void updateFetchButton();

    QTreeWidget* m_tree = nullptr;
    QPushButton* m_fetchButton = nullptr;
};

}
