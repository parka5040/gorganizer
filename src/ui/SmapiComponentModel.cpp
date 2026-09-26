#include "SmapiComponentModel.h"
#include "ModDependencyText.h"
#include "ThemeManager.h"

#include <QFont>
#include <algorithm>

namespace gorganizer {

namespace {

QString displayName(const GrpcModComponent& component)
{
    return component.name.isEmpty() ? component.folder : component.name;
}

QString updateUrlOf(const GrpcModComponent& component)
{
    return component.updateUrl.startsWith(QLatin1String("https://")) ? component.updateUrl : QString();
}

}

SmapiComponentModel::SmapiComponentModel(QObject* parent)
    : QAbstractTableModel(parent)
{
}

int SmapiComponentModel::rowCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : static_cast<int>(m_components.size());
}

int SmapiComponentModel::columnCount(const QModelIndex& parent) const
{
    return parent.isValid() ? 0 : SmapiColCount;
}

QVariant SmapiComponentModel::headerData(int section, Qt::Orientation orientation, int role) const
{
    if (role != Qt::DisplayRole || orientation != Qt::Horizontal)
        return {};
    switch (section) {
    case SmapiColName: return QStringLiteral("Name");
    case SmapiColUniqueId: return QStringLiteral("UniqueID");
    case SmapiColVersion: return QStringLiteral("Version");
    case SmapiColType: return QStringLiteral("Type");
    case SmapiColStatus: return QStringLiteral("Status");
    case SmapiColProvider: return QStringLiteral("Provided by");
    case SmapiColUpdate: return QStringLiteral("Update");
    default: return {};
    }
}

QVariant SmapiComponentModel::data(const QModelIndex& idx, int role) const
{
    if (!idx.isValid() || idx.row() < 0 || idx.row() >= static_cast<int>(m_components.size()))
        return {};
    const GrpcModComponent& c = m_components[idx.row()];
    const QString updateUrl = updateUrlOf(c);

    switch (role) {
    case UniqueIdRole:
        return c.uniqueId;
    case ProviderModRole:
        return c.providerMod;
    case SeverityRole:
        return static_cast<int>(componentSeverity(c));
    case UpdateVersionRole:
        return c.updateVersion;
    case UpdateUrlRole:
        return updateUrl;
    case BundledRole:
        return c.bundled;
    case FailedRole:
        return c.failed;
    case Qt::DisplayRole:
        switch (idx.column()) {
        case SmapiColName: return displayName(c);
        case SmapiColUniqueId: return c.uniqueId;
        case SmapiColVersion: return c.version;
        case SmapiColType: return componentKindText(c.kind);
        case SmapiColStatus: return componentStatusText(c);
        case SmapiColProvider: return componentProviderText(c);
        case SmapiColUpdate:
            if (c.updateVersion.isEmpty())
                return QString();
            return c.updateStale ? QStringLiteral("%1 (cached)").arg(c.updateVersion) : c.updateVersion;
        default: return {};
        }
    case Qt::ToolTipRole:
        switch (idx.column()) {
        case SmapiColName:
            if (c.bundled)
                return plainToolTip(QStringLiteral("Mods/%1 — installed with SMAPI").arg(c.folder));
            return plainToolTip(QStringLiteral("Mods/%1").arg(c.folder));
        case SmapiColStatus:
            return plainToolTip(statusTooltip(c));
        case SmapiColUpdate:
            if (c.updateVersion.isEmpty())
                return {};
            if (updateUrl.isEmpty())
                return plainToolTip(
                    QStringLiteral("Version %1 is available; smapi.io knows no download page.").arg(c.updateVersion));
            return plainToolTip(QStringLiteral("Click to open %1").arg(updateUrl));
        default:
            return {};
        }
    case Qt::ForegroundRole: {
        const Palette& pal = ThemeManager::currentPalette();
        if (idx.column() == SmapiColUpdate && !updateUrl.isEmpty())
            return pal.accent;
        if (idx.column() == SmapiColStatus) {
            const DependencySeverity severity = componentSeverity(c);
            if (severity == DependencySeverityError)
                return pal.errorFg;
            if (severity == DependencySeverityWarning)
                return pal.warningFg;
        }
        if (c.bundled)
            return pal.textMuted;
        return {};
    }
    case Qt::FontRole:
        if (idx.column() == SmapiColUpdate && !updateUrl.isEmpty()) {
            QFont f;
            f.setUnderline(true);
            return f;
        }
        if (c.bundled) {
            QFont f;
            f.setItalic(true);
            return f;
        }
        return {};
    default:
        return {};
    }
}

void SmapiComponentModel::setReport(const GrpcModDependencyReport& report)
{
    beginResetModel();
    m_components = report.components;
    m_names = dependencyNames(report);
    std::stable_sort(m_components.begin(), m_components.end(),
                     [](const GrpcModComponent& a, const GrpcModComponent& b) {
                         const int sa = componentSeverity(a);
                         const int sb = componentSeverity(b);
                         if (sa != sb)
                             return sa > sb;
                         if (a.bundled != b.bundled)
                             return !a.bundled;
                         return displayName(a).compare(displayName(b), Qt::CaseInsensitive) < 0;
                     });
    endResetModel();
}

void SmapiComponentModel::clear()
{
    beginResetModel();
    m_components.clear();
    m_names.clear();
    endResetModel();
}

QString SmapiComponentModel::statusTooltip(const GrpcModComponent& component) const
{
    QStringList lines;
    for (const auto& issue : component.issues)
        lines.append(QStringLiteral("• %1").arg(issueDescription(issue, m_names)));
    if (lines.isEmpty()) {
        if (component.failed)
            return QStringLiteral("SMAPI will not load this mod.");
        return QStringLiteral("SMAPI can load this mod.");
    }
    if (component.failed)
        lines.prepend(QStringLiteral("SMAPI will not load this mod:"));
    return lines.join(QLatin1Char('\n'));
}

}
