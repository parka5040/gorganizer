#include "ModListRowDelegate.h"
#include "ModListModel.h"
#include "ModDependencyText.h"
#include "ThemeManager.h"

#include <QApplication>
#include <QPainter>

namespace gorganizer {

ModListRowDelegate::ModListRowDelegate(QObject* parent)
    : QStyledItemDelegate(parent)
{
}

// Adds a mod row's SMAPI dependency or update indicator to its style option.
void ModListRowDelegate::applyDependencyIcon(QStyleOptionViewItem& opt, const QModelIndex& index) const
{
    const int severity = index.data(ModListModel::DependencyIssueRole).toInt();
    const bool update = !index.data(ModListModel::UpdateAvailableRole).toString().isEmpty();
    if (severity <= DependencySeverityNone && !update)
        return;
    const QWidget* widget = opt.widget;
    QStyle* style = widget ? widget->style() : QApplication::style();
    QStyle::StandardPixmap pixmap = QStyle::SP_ArrowUp;
    if (severity >= DependencySeverityError)
        pixmap = QStyle::SP_MessageBoxCritical;
    else if (severity == DependencySeverityWarning)
        pixmap = QStyle::SP_MessageBoxWarning;
    const int extent = style->pixelMetric(QStyle::PM_SmallIconSize, nullptr, widget);
    opt.icon = style->standardIcon(pixmap, nullptr, widget);
    opt.decorationSize = QSize(extent, extent);
    opt.features |= QStyleOptionViewItem::HasDecoration;
}

// Resolves kind/conflict/tint styling from the current palette at paint time, then defers to the style.
void ModListRowDelegate::paint(QPainter* painter, const QStyleOptionViewItem& option,
                               const QModelIndex& index) const
{
    QStyleOptionViewItem opt = option;
    initStyleOption(&opt, index);

    const Palette& pal = ThemeManager::currentPalette();
    const int kind = index.data(ModListModel::RowKindRole).toInt();

    if (kind == RowKindSeparator) {
        opt.backgroundBrush = QBrush(pal.surface);
        if (index.column() == ModColName)
            opt.palette.setColor(QPalette::Text, pal.accent);
    } else if (kind == RowKindOverwrite) {
        if (index.column() == ModColPriority)
            opt.palette.setColor(QPalette::Text, pal.textMuted);
    } else {
        const int tint = index.data(ModListModel::TintRole).toInt();
        if (tint == ModListModel::TintLosesToSelection)
            opt.backgroundBrush = QBrush(pal.errorBg);
        else if (tint == ModListModel::TintBeatsSelection)
            opt.backgroundBrush = QBrush(pal.successBg);
        if (index.column() == ModColConflicts) {
            const QString mark = index.data(ModListModel::ConflictMarkRole).toString();
            if (mark == "+-")
                opt.palette.setColor(QPalette::Text, pal.warningFg);
            else if (mark == "+")
                opt.palette.setColor(QPalette::Text, pal.successFg);
            else if (mark == "-")
                opt.palette.setColor(QPalette::Text, pal.errorFg);
        }
        if (index.column() == ModColName)
            applyDependencyIcon(opt, index);
    }

    const QWidget* widget = option.widget;
    QStyle* style = widget ? widget->style() : QApplication::style();
    style->drawControl(QStyle::CE_ItemViewItem, &opt, painter, widget);
}

}
