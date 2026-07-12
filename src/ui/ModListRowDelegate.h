#pragma once

#include <QStyledItemDelegate>

namespace gorganizer {

class ModListRowDelegate : public QStyledItemDelegate {
    Q_OBJECT
public:
    explicit ModListRowDelegate(QObject* parent = nullptr);

    void paint(QPainter* painter, const QStyleOptionViewItem& option,
               const QModelIndex& index) const override;
};

}
