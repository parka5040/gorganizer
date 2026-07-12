#pragma once

#include <QStyledItemDelegate>

namespace gorganizer {

class PluginRowDelegate : public QStyledItemDelegate {
    Q_OBJECT
public:
    explicit PluginRowDelegate(QObject* parent = nullptr);

    void paint(QPainter* painter, const QStyleOptionViewItem& option,
               const QModelIndex& index) const override;
};

}
