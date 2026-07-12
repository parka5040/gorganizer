#pragma once

#include <QStyledItemDelegate>

namespace gorganizer {

class DownloadsRowDelegate : public QStyledItemDelegate {
    Q_OBJECT
public:
    explicit DownloadsRowDelegate(QObject* parent = nullptr);

    void paint(QPainter* painter, const QStyleOptionViewItem& option,
               const QModelIndex& index) const override;

    QSize sizeHint(const QStyleOptionViewItem& option,
                   const QModelIndex& index) const override;
};

}
