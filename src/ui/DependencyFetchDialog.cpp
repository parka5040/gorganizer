#include "DependencyFetchDialog.h"
#include "ModDependencyText.h"

#include <QDialogButtonBox>
#include <QHeaderView>
#include <QLabel>
#include <QPushButton>
#include <QTreeWidget>
#include <QVBoxLayout>

namespace gorganizer {

namespace {

enum FetchColumn { ColDependency = 0, ColRequiredBy = 1, ColNexus = 2, ColStatus = 3, ColCount = 4 };

enum FetchItemRole { UniqueIdItemRole = Qt::UserRole + 1 };

bool metadataKnown(const GrpcMissingDependency& dep)
{
    return dep.resolvable || dep.nexusId > 0 || !dep.name.isEmpty() || !dep.url.isEmpty();
}

QLabel* linkLabel(const QString& url, const QString& text)
{
    auto* label = new QLabel(QStringLiteral("<a href=\"%1\">%2</a>").arg(url.toHtmlEscaped(), text.toHtmlEscaped()));
    label->setTextFormat(Qt::RichText);
    label->setOpenExternalLinks(true);
    label->setToolTip(plainToolTip(url));
    return label;
}

}

DependencyFetchDialog::DependencyFetchDialog(const std::vector<GrpcMissingDependency>& dependencies,
                                             const QHash<QString, QString>& names, const QString& profileName,
                                             QWidget* parent)
    : QDialog(parent)
{
    setWindowTitle(QStringLiteral("Fetch Missing Dependencies"));
    resize(760, 420);
    auto* layout = new QVBoxLayout(this);

    auto* intro = new QLabel(
        QStringLiteral("gorganizer downloads the main file of each checked mod from Nexus Mods when you have "
                       "Nexus Premium, or opens its Nexus page so you can use \"Mod Manager Download\". "
                       "Finished downloads are installed and then enabled in profile \"%1\".")
            .arg(profileName));
    intro->setTextFormat(Qt::PlainText);
    intro->setWordWrap(true);
    layout->addWidget(intro);

    m_tree = new QTreeWidget;
    m_tree->setColumnCount(ColCount);
    m_tree->setHeaderLabels({QStringLiteral("Dependency"), QStringLiteral("Required by"),
                             QStringLiteral("Nexus page"), QStringLiteral("Status")});
    m_tree->setRootIsDecorated(false);
    m_tree->setUniformRowHeights(true);
    m_tree->setSelectionMode(QAbstractItemView::NoSelection);
    layout->addWidget(m_tree, 1);

    for (const auto& dep : dependencies) {
        auto* item = new QTreeWidgetItem;
        const QString title = dep.name.isEmpty() ? dep.uniqueId : dep.name;
        item->setText(ColDependency, title);
        item->setData(ColDependency, UniqueIdItemRole, dep.uniqueId);
        QString idTip = QStringLiteral("UniqueID: %1").arg(dep.uniqueId);
        if (!dep.minimumVersion.isEmpty())
            idTip += QStringLiteral("\nMinimum version: %1").arg(dep.minimumVersion);
        item->setToolTip(ColDependency, plainToolTip(idTip));

        QStringList requirers;
        for (const auto& id : dep.requiredBy)
            requirers.append(dependencyDisplayName(id, names));
        item->setText(ColRequiredBy, requirers.join(QStringLiteral(", ")));
        item->setToolTip(ColRequiredBy, plainToolTip(requirers.join(QLatin1Char('\n'))));

        const bool known = metadataKnown(dep);
        const bool pickable = dep.resolvable || !known;
        QString status;
        if (dep.resolvable)
            status = dep.stale ? QStringLiteral("Nexus page known (cached)") : QStringLiteral("Nexus page known");
        else if (!known)
            status = QStringLiteral("Not looked up yet; smapi.io is asked when you fetch");
        else
            status = QStringLiteral("No Nexus page known; download it manually");
        item->setText(ColStatus, status);
        if (!pickable)
            item->setToolTip(ColStatus, plainToolTip(QStringLiteral("smapi.io does not list a Nexus Mods page for "
                                                                    "this mod, so gorganizer cannot fetch it. Use "
                                                                    "the link, if any, to get it yourself.")));

        Qt::ItemFlags flags = Qt::ItemIsEnabled;
        if (pickable)
            flags |= Qt::ItemIsUserCheckable;
        item->setFlags(flags);
        item->setCheckState(ColDependency, pickable ? Qt::Checked : Qt::Unchecked);
        if (!pickable)
            item->setDisabled(true);
        m_tree->addTopLevelItem(item);

        if (dep.url.startsWith(QLatin1String("https://")))
            m_tree->setItemWidget(item, ColNexus, linkLabel(dep.url, dep.resolvable ? QStringLiteral("Nexus Mods")
                                                                                   : QStringLiteral("Mod page")));
        else
            item->setText(ColNexus, QStringLiteral("—"));
    }
    for (int c = 0; c < ColCount; ++c)
        m_tree->resizeColumnToContents(c);

    auto* buttons = new QDialogButtonBox(QDialogButtonBox::Cancel);
    m_fetchButton = buttons->addButton(QStringLiteral("Fetch"), QDialogButtonBox::AcceptRole);
    layout->addWidget(buttons);
    connect(buttons, &QDialogButtonBox::accepted, this, &QDialog::accept);
    connect(buttons, &QDialogButtonBox::rejected, this, &QDialog::reject);
    connect(m_tree, &QTreeWidget::itemChanged, this, &DependencyFetchDialog::onItemChanged);
    updateFetchButton();
}

QStringList DependencyFetchDialog::selectedIds() const
{
    QStringList ids;
    for (int i = 0; i < m_tree->topLevelItemCount(); ++i) {
        const QTreeWidgetItem* item = m_tree->topLevelItem(i);
        if (!item->isDisabled() && item->checkState(ColDependency) == Qt::Checked)
            ids.append(item->data(ColDependency, UniqueIdItemRole).toString());
    }
    return ids;
}

void DependencyFetchDialog::onItemChanged(QTreeWidgetItem*, int column)
{
    if (column == ColDependency)
        updateFetchButton();
}

void DependencyFetchDialog::updateFetchButton()
{
    m_fetchButton->setEnabled(!selectedIds().isEmpty());
}

}
