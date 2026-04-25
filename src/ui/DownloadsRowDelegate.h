#pragma once

#include <QStyledItemDelegate>

namespace gorganizer {

// Delegate for the DownloadsModel's Status column. Renders a
// color-coded progress bar plus phase label in-place, so a row can
// transition from Downloading → Installing → Installed without any
// widget swaps or full-row redraws.
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
