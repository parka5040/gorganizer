#include "PluginListWidget.h"
#include "PluginRowDelegate.h"
#include "GrpcClient.h"
#include "ThemeManager.h"
#include "Dialogs.h"

#include <QVBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QDropEvent>
#include <QDebug>

namespace gorganizer {

LoadOrderTreeView::LoadOrderTreeView(PluginListWidget* owner, QWidget* parent)
    : QTreeView(parent)
    , m_owner(owner)
{
}

// Resolves the drop row from the cursor position; past-the-end lands after the last row.
int LoadOrderTreeView::dropTargetRow(QDropEvent* event) const
{
    auto pos = event->position().toPoint();
    auto idx = indexAt(pos);

    if (!idx.isValid())
        return model()->rowCount();

    auto rect = visualRect(idx);
    bool aboveHalf = (pos.y() < rect.center().y());
    return aboveHalf ? idx.row() : idx.row() + 1;
}

// Applies a legal load-order move, explaining any rejection instead of silently ignoring it.
void LoadOrderTreeView::dropEvent(QDropEvent* event)
{
    if (!model())
        return;

    if (!(m_owner->m_sortColumn == PluginColIndex && m_owner->m_sortOrder == Qt::AscendingOrder)) {
        event->ignore();
        dialogs::info(this, "Can't reorder",
            "Plugins can only be reordered while sorted by Index (load order). "
            "Click the Index column header to sort ascending, then drag.");
        return;
    }

    auto selected = selectionModel()->selectedRows(PluginColName);
    if (selected.isEmpty()) {
        event->ignore();
        return;
    }

    int sourceRow = selected.first().row();
    int destRow = dropTargetRow(event);

    PluginListModel* m = m_owner->m_model;
    QString reason;
    if (!m->canMove(sourceRow, destRow, &reason)) {
        event->ignore();
        if (!reason.isEmpty())
            dialogs::info(this, "Can't move plugin", reason);
        return;
    }

    int landing = m->moveRowTo(sourceRow, destRow);
    if (landing < 0) {
        event->ignore();
        return;
    }

    m->revalidateOrderingIssues();
    m_owner->persistLoadoutToDaemon();

    selectionModel()->select(m->index(landing, 0),
                             QItemSelectionModel::ClearAndSelect | QItemSelectionModel::Rows);

    event->setDropAction(Qt::CopyAction);
    event->accept();
}

PluginListWidget::PluginListWidget(QWidget* parent)
    : QWidget(parent)
{
    auto* layout = new QVBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);

    auto* titleLabel = new QLabel("Plugins");
    titleLabel->setStyleSheet("font-weight: bold;");
    layout->addWidget(titleLabel);

    m_model = new PluginListModel(this);
    connect(m_model, &PluginListModel::activationEdited,
            this, &PluginListWidget::persistLoadoutToDaemon);

    m_view = new LoadOrderTreeView(this);
    m_view->setModel(m_model);
    m_view->setItemDelegate(new PluginRowDelegate(m_view));
    m_view->setRootIsDecorated(false);
    m_view->setSelectionMode(QAbstractItemView::SingleSelection);
    m_view->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_view->setDragEnabled(true);
    m_view->setAcceptDrops(true);
    m_view->setDropIndicatorShown(true);
    m_view->setDragDropMode(QAbstractItemView::DragDrop);
    m_view->setDefaultDropAction(Qt::MoveAction);

    m_view->setSortingEnabled(false);
    m_view->header()->setSectionsClickable(true);
    m_view->header()->setSortIndicatorShown(true);
    m_view->header()->setSortIndicator(PluginColIndex, Qt::AscendingOrder);

    m_view->header()->setSectionResizeMode(PluginColIndex, QHeaderView::ResizeToContents);
    m_view->header()->setSectionResizeMode(PluginColName, QHeaderView::Stretch);
    m_view->header()->setSectionResizeMode(PluginColType, QHeaderView::ResizeToContents);
    m_view->header()->setSectionResizeMode(PluginColStatus, QHeaderView::Fixed);
    m_view->header()->resizeSection(PluginColStatus, 24);

    connect(m_view->header(), &QHeaderView::sectionClicked,
            this, &PluginListWidget::onHeaderClicked);

    connect(ThemeManager::instance(), &ThemeManager::themeChanged,
            this, [this](const Palette&) { m_view->viewport()->update(); });

    layout->addWidget(m_view);

    m_placeholder = new QWidget;
    auto* placeholderLayout = new QVBoxLayout(m_placeholder);
    auto* placeholderLabel = new QLabel("No game selected.");
    placeholderLabel->setAlignment(Qt::AlignCenter);
    placeholderLabel->setObjectName("hintLabel");
    placeholderLayout->addWidget(placeholderLabel);
    layout->addWidget(m_placeholder);

    m_view->hide();
    m_placeholder->show();
}

// Cycles asc → desc → back to load-order ascending; drag is only enabled in load-order ascending.
void PluginListWidget::onHeaderClicked(int column)
{
    if (m_sortColumn == column) {
        if (m_sortOrder == Qt::AscendingOrder) {
            m_sortOrder = Qt::DescendingOrder;
        } else {
            m_sortColumn = PluginColIndex;
            m_sortOrder = Qt::AscendingOrder;
            m_view->header()->setSortIndicator(PluginColIndex, Qt::AscendingOrder);
            m_model->restoreLoadOrder();
            m_view->setDragEnabled(true);
            return;
        }
    } else {
        m_sortColumn = column;
        m_sortOrder = Qt::AscendingOrder;
    }

    m_view->header()->setSortIndicator(m_sortColumn, m_sortOrder);

    if (m_sortColumn == PluginColIndex && m_sortOrder == Qt::AscendingOrder) {
        m_model->restoreLoadOrder();
        m_view->setDragEnabled(true);
    } else {
        m_view->setDragEnabled(false);
        m_model->sortBy(m_sortColumn, m_sortOrder);
    }
}

void PluginListWidget::setModsDir(const QString& modsDir)
{
    Q_UNUSED(modsDir);
}

void PluginListWidget::loadForGame(const GameInfo& game)
{
    m_game = game;
    m_model->clear();
    m_sortColumn = PluginColIndex;
    m_sortOrder = Qt::AscendingOrder;
    m_view->header()->setSortIndicator(PluginColIndex, Qt::AscendingOrder);

    if (!game.detected) {
        m_view->hide();
        m_placeholder->show();
        resubscribeStream();
        return;
    }
    m_view->hide();
    m_placeholder->show();
    resubscribeStream();
}

void PluginListWidget::refresh()
{
    if (m_game.detected)
        loadForGame(m_game);
    resubscribeStream();
}

void PluginListWidget::setGrpcClient(GrpcClient* grpc)
{
    if (m_grpc == grpc) return;
    if (m_grpc) {
        disconnect(m_grpc, nullptr, this, nullptr);
        m_grpc->unsubscribePluginStatus();
    }
    m_grpc = grpc;
    if (!m_grpc) return;
    connect(m_grpc, &GrpcClient::pluginStatusSnapshot,
            this, [this](const std::vector<GrpcPluginStatus>& plugins) {
                m_model->applySnapshot(plugins, m_game.shortName);
                m_view->setVisible(!plugins.empty());
                m_placeholder->setVisible(plugins.empty());
            });
    connect(m_grpc, &GrpcClient::pluginStatusUpdate,
            this, [this](const GrpcPluginStatus& plugin) {
                m_model->applyUpdate(plugin);
            });
    resubscribeStream();
}

void PluginListWidget::setActiveProfile(const QString& profileName)
{
    if (m_activeProfile == profileName) return;
    m_activeProfile = profileName;
    m_model->clear();
    m_view->hide();
    m_placeholder->show();
    resubscribeStream();
}

void PluginListWidget::resubscribeStream()
{
    if (!m_grpc) return;
    if (!m_game.detected || m_activeProfile.isEmpty()) {
        m_grpc->unsubscribePluginStatus();
        return;
    }
    m_grpc->subscribePluginStatus(m_game.shortName, m_activeProfile);
}

void PluginListWidget::persistLoadoutToDaemon()
{
    if (!m_grpc || !m_game.detected || m_activeProfile.isEmpty())
        return;
    const auto loadout = m_model->orderedLoadout();
    QString err;
    if (!m_grpc->setPluginLoadout(m_game.shortName, m_activeProfile, loadout, err)) {
        qWarning().noquote() << "setPluginLoadout failed:" << err;
        dialogs::warn(this, "Plugin state not saved",
            QString("The plugin order and activation state could not be saved:\n\n%1").arg(err));
        resubscribeStream();
        return;
    }
    resubscribeStream();
}

}
