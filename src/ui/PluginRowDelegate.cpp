#include "PluginRowDelegate.h"
#include "PluginListModel.h"
#include "PluginScanner.h"
#include "GrpcTypes.h"
#include "ThemeManager.h"

#include <QApplication>
#include <QPainter>

namespace gorganizer {

PluginRowDelegate::PluginRowDelegate(QObject* parent)
    : QStyledItemDelegate(parent)
{
}

// Warning triangle filled with a status color; outline/tick track the theme text color for visibility.
static QPixmap statusIcon(const QColor& color, const Palette& pal)
{
    QPixmap pm(16, 16);
    pm.fill(Qt::transparent);
    QPainter p(&pm);
    p.setRenderHint(QPainter::Antialiasing, true);
    p.setBrush(color);
    p.setPen(Qt::NoPen);
    QPolygon tri;
    tri << QPoint(8, 1) << QPoint(15, 14) << QPoint(1, 14);
    p.drawPolygon(tri);
    QColor outline = pal.text;
    outline.setAlpha(200);
    p.setPen(QPen(outline, 1));
    p.setBrush(Qt::NoBrush);
    p.drawPolygon(tri);
    QColor tick = pal.text;
    tick.setAlpha(220);
    p.setPen(QPen(tick, 2));
    p.drawLine(QPoint(8, 5), QPoint(8, 10));
    p.drawPoint(QPoint(8, 12));
    return pm;
}

// Muted dot shown while soft-dependency analysis is still pending for the plugin.
static QPixmap pendingIcon(const Palette& pal)
{
    QPixmap pm(16, 16);
    pm.fill(Qt::transparent);
    QPainter p(&pm);
    p.setRenderHint(QPainter::Antialiasing, true);
    QColor fill = pal.textMuted;
    fill.setAlpha(180);
    p.setBrush(fill);
    p.setPen(Qt::NoPen);
    p.drawEllipse(2, 2, 12, 12);
    return pm;
}

// Row-tint background for the worst dep-issue kind.
static QColor tintFor(int kind, const Palette& pal)
{
    switch (kind) {
    case GrpcDepMasterAbsent:
    case GrpcDepMasterOutOfOrder:
        return pal.errorBg;
    case GrpcDepMasterDisabled:
        return pal.warningBg;
    case GrpcDepSoftMissing:
        return pal.infoBg;
    default:
        return QColor(0, 0, 0, 0);
    }
}

// Plugin-type foreground from the current palette.
static QColor typeColor(int type, const Palette& pal)
{
    switch (type) {
    case PluginEntry::ESM: return pal.infoFg;
    case PluginEntry::ESL: return pal.successFg;
    case PluginEntry::ESP: return pal.text;
    default: return pal.text;
    }
}

// Resolves dep-issue tints, type foregrounds, and status icons from the current palette at paint time.
void PluginRowDelegate::paint(QPainter* painter, const QStyleOptionViewItem& option,
                              const QModelIndex& index) const
{
    QStyleOptionViewItem opt = option;
    initStyleOption(&opt, index);

    const Palette& pal = ThemeManager::currentPalette();
    const int worst = index.data(PluginListModel::DepWorstKindRole).toInt();

    if (worst != 0)
        opt.backgroundBrush = QBrush(tintFor(worst, pal));

    if (index.column() == PluginColName || index.column() == PluginColType) {
        const int type = index.data(PluginListModel::PluginTypeRole).toInt();
        opt.palette.setColor(QPalette::Text, typeColor(type, pal));
    }

    if (index.column() == PluginColStatus) {
        QPixmap pm;
        if (worst == GrpcDepMasterAbsent || worst == GrpcDepMasterOutOfOrder)
            pm = statusIcon(pal.error, pal);
        else if (worst == GrpcDepMasterDisabled)
            pm = statusIcon(pal.warning, pal);
        else if (worst == GrpcDepSoftMissing)
            pm = statusIcon(pal.info, pal);
        else if (index.data(PluginListModel::SoftPendingRole).toBool())
            pm = pendingIcon(pal);
        if (!pm.isNull()) {
            opt.icon = QIcon(pm);
            opt.decorationSize = pm.size();
            opt.features |= QStyleOptionViewItem::HasDecoration;
        }
    }

    const QWidget* widget = option.widget;
    QStyle* style = widget ? widget->style() : QApplication::style();
    style->drawControl(QStyle::CE_ItemViewItem, &opt, painter, widget);
}

}
