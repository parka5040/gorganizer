#include "DownloadsRowDelegate.h"
#include "DownloadsModel.h"

#include <QApplication>
#include <QPainter>
#include <QPen>

namespace gorganizer {

DownloadsRowDelegate::DownloadsRowDelegate(QObject* parent)
    : QStyledItemDelegate(parent)
{
}

static QColor phaseColor(DownloadPhase phase, bool merged, const QPalette& palette)
{
    if (merged && phase == DownloadPhase::Installed)
        return QColor(120, 140, 200);
    switch (phase) {
        case DownloadPhase::Downloading: return QColor(70, 140, 220);
        case DownloadPhase::Downloaded:  return QColor(120, 170, 220);
        case DownloadPhase::Installing:  return QColor(220, 170, 60);
        case DownloadPhase::Installed:   return QColor(90, 170, 100);
        case DownloadPhase::Uninstalled: return palette.color(QPalette::Mid);
        case DownloadPhase::Failed:      return QColor(200, 70, 70);
        default:                         return palette.color(QPalette::Mid);
    }
}

static QString phaseText(DownloadPhase phase, bool merged)
{
    if (merged && phase == DownloadPhase::Installed)
        return "Merged";
    switch (phase) {
        case DownloadPhase::Downloading: return "Downloading";
        case DownloadPhase::Downloaded:  return "Downloaded";
        case DownloadPhase::Installing:  return "Installing";
        case DownloadPhase::Installed:   return "Installed";
        case DownloadPhase::Uninstalled: return "Uninstalled";
        case DownloadPhase::Failed:      return "Failed";
        default:                         return "—";
    }
}

void DownloadsRowDelegate::paint(QPainter* painter, const QStyleOptionViewItem& option,
                                 const QModelIndex& index) const
{
    if (index.column() != DownloadsModel::ColStatus) {
        QStyledItemDelegate::paint(painter, option, index);
        return;
    }

    int phaseInt = index.data(DownloadsModel::PhaseRole).toInt();
    auto phase = static_cast<DownloadPhase>(phaseInt);
    int pct = index.data(DownloadsModel::ProgressRole).toInt();
    bool merged = index.data(DownloadsModel::MergedRole).toBool();

    QStyleOptionViewItem opt = option;
    initStyleOption(&opt, index);
    opt.text.clear();
    QApplication::style()->drawControl(QStyle::CE_ItemViewItem, &opt, painter);

    const QRect r = option.rect.adjusted(4, 4, -4, -4);
    const QString label = phaseText(phase, merged);

    const bool terminal = (phase == DownloadPhase::Installed
                        || phase == DownloadPhase::Downloaded
                        || phase == DownloadPhase::Uninstalled
                        || phase == DownloadPhase::Failed
                        || phase == DownloadPhase::Unknown);

    painter->save();
    painter->setRenderHint(QPainter::Antialiasing, true);

    if (terminal) {
        QColor chipColor = phaseColor(phase, merged, option.palette);
        chipColor.setAlpha(80);
        painter->setBrush(chipColor);
        painter->setPen(Qt::NoPen);
        QRect chip = r.adjusted(0, (r.height() - 18) / 2, 0, -(r.height() - 18) / 2);
        chip.setWidth(qMin(chip.width(), 120));
        painter->drawRoundedRect(chip, 6, 6);
        painter->setPen(option.palette.color(QPalette::Text));
        painter->drawText(chip, Qt::AlignCenter, label);
    } else {
        const QColor chunkColor = phaseColor(phase, merged, option.palette);
        QColor trackColor = option.palette.color(QPalette::Base);
        if (trackColor.alpha() == 0)
            trackColor = option.palette.color(QPalette::Window);

        painter->setPen(QPen(option.palette.color(QPalette::Mid), 1));
        painter->setBrush(trackColor);
        painter->drawRoundedRect(r, 3, 3);

        if (pct > 0) {
            QRect chunkRect(r.x(), r.y(),
                            qMax(0, r.width() * qMin(pct, 100) / 100),
                            r.height());
            painter->setPen(Qt::NoPen);
            painter->setBrush(chunkColor);
            painter->setClipRect(r.adjusted(1, 1, -1, -1));
            painter->drawRoundedRect(chunkRect, 3, 3);
            painter->setClipping(false);
        }

        const QString text = (pct < 0)
            ? label
            : QString("%1  %2%").arg(label).arg(pct);
        painter->setPen(option.palette.color(QPalette::Text));
        painter->drawText(r, Qt::AlignCenter, text);
    }
    painter->restore();
}

QSize DownloadsRowDelegate::sizeHint(const QStyleOptionViewItem& option,
                                     const QModelIndex& index) const
{
    QSize sz = QStyledItemDelegate::sizeHint(option, index);
    sz.setHeight(qMax(sz.height(), 26));
    sz.setWidth(qMax(sz.width(), 160));
    return sz;
}

} // namespace gorganizer
