#pragma once

#include <QStyledItemDelegate>

namespace gorganizer {

// Renders a color-coded progress bar with phase label for the Status column.
class DownloadsRowDelegate : public QStyledItemDelegate {
    Q_OBJECT
public:
    explicit DownloadsRowDelegate(QObject* parent = nullptr);

    void paint(QPainter* painter, const QStyleOptionViewItem& option,
               const QModelIndex& index) const override;

    QSize sizeHint(const QStyleOptionViewItem& option,
                   const QModelIndex& index) const override;
};

} // namespace gorganizer
