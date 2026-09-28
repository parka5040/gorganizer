#include "ModListWidget.h"
#include "WindowFit.h"
#include "InstallController.h"
#include "ModListSaveQueue.h"
#include "ModListRowDelegate.h"
#include "ThemeManager.h"
#include "Dialogs.h"
#include "InstallErrorText.h"
#include "ErrorPresenter.h"
#include "ModDependencyText.h"
#include "SafeLinks.h"

#include <QApplication>
#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QDropEvent>
#include <QDragEnterEvent>
#include <QDragMoveEvent>
#include <QMimeData>
#include <QComboBox>
#include <QCheckBox>
#include <QPushButton>
#include <QMenu>
#include <QInputDialog>
#include <QDesktopServices>
#include <QUrl>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QMessageBox>
#include <QMainWindow>
#include <QProgressDialog>
#include <QStatusBar>
#include <QMetaObject>
#include <QPointer>
#include <QSet>
#include <QThreadPool>
#include <QTimer>
#include <QTreeWidget>
#include <QDialog>
#include <QListWidget>
#include <QLineEdit>
#include <QDialogButtonBox>
#include <algorithm>
#include <limits>

namespace gorganizer {

static quint64 parseHexIndex(const QString& s)
{
    if (s.isEmpty()) return 0;
    bool ok = false;
    quint64 v = s.toULongLong(&ok, 16);
    return ok ? v : 0;
}

static QString formatHexIndex(quint64 v)
{
    return QString("%1").arg(v, 16, 16, QLatin1Char('0'));
}

static constexpr quint64 kTrueIndexStep = 0x10;
static constexpr int kProfileListRetries = 5;
static constexpr int kProfileListRetryBaseMs = 1000;

static QString modActionError(const QString& error, bool submittedWhileConnected)
{
    if (!submittedWhileConnected && error == QLatin1String("not connected"))
        return error;
    if (submittedWhileConnected && error.isEmpty())
        return QStringLiteral("deadline exceeded");
    const QString token = parseInstallError(error).token;
    if (!token.isEmpty() && token != QLatin1String("unavailable")
        && token != QLatin1String("cancelled") && token != QLatin1String("timeout"))
        return error;
    if (error.contains(QLatin1String("deadline exceeded"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("not connected"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("socket"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("connection"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("transport"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("channel"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("unavailable"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("cancelled"), Qt::CaseInsensitive))
        return QStringLiteral("deadline exceeded");
    return error;
}

// Reports whether two catalog scans would produce the same rows in the same order.
static bool sameRows(const std::vector<ModMetadata>& a, const std::vector<ModMetadata>& b)
{
    if (a.size() != b.size())
        return false;
    for (size_t i = 0; i < a.size(); ++i) {
        const ModMetadata& x = a[i];
        const ModMetadata& y = b[i];
        if (x.folder != y.folder || x.name != y.name || x.category != y.category || x.version != y.version
            || x.enabled != y.enabled || x.trueIndex != y.trueIndex || x.visualIndex != y.visualIndex
            || x.separator != y.separator)
            return false;
    }
    return true;
}

ModListTreeView::ModListTreeView(ModListWidget* owner, QWidget* parent)
    : QTreeView(parent)
    , m_owner(owner)
{
}

// Resolves the drop row: separators land at group bottom, past-the-end lands just above Overwrite.
int ModListTreeView::dropTargetRow(QDropEvent* event) const
{
    const ModListModel* m = m_owner->m_model;
    auto pos = event->position().toPoint();
    auto idx = indexAt(pos);

    int overwriteRow = m->overwriteRow();
    int floor = (overwriteRow >= 0) ? overwriteRow : m->rowCount();

    if (!idx.isValid())
        return floor;

    int kind = m->rowAt(idx.row()).kind;
    if (kind == RowKindSeparator) {
        for (int r = idx.row() + 1; r < m->rowCount(); ++r) {
            int k = m->rowAt(r).kind;
            if (k == RowKindSeparator || k == RowKindOverwrite)
                return r;
        }
        return floor;
    }

    if (kind == RowKindOverwrite)
        return idx.row();

    auto rect = visualRect(idx);
    bool aboveHalf = (pos.y() < rect.center().y());
    int target = aboveHalf ? idx.row() : idx.row() + 1;
    if (target > floor) target = floor;
    return target;
}

void ModListTreeView::dragEnterEvent(QDragEnterEvent* event)
{
    if (event->mimeData()->hasUrls()) {
        event->ignore();
        return;
    }
    QTreeView::dragEnterEvent(event);
}

void ModListTreeView::dragMoveEvent(QDragMoveEvent* event)
{
    if (event->mimeData()->hasUrls()) {
        event->ignore();
        return;
    }
    QTreeView::dragMoveEvent(event);
}

// Moves the multi-selected draggable rows to the computed target, then persists the new order.
void ModListTreeView::dropEvent(QDropEvent* event)
{
    if (event->mimeData()->hasUrls()) {
        event->ignore();
        return;
    }
    if (!model())
        return;

    if (!(m_owner->m_sortColumn == ModColPriority && m_owner->m_sortOrder == Qt::AscendingOrder)) {
        event->ignore();
        return;
    }

    ModListModel* m = m_owner->m_model;
    int count = m->rowCount();

    auto pos = event->position().toPoint();
    auto cursorIdx = indexAt(pos);
    int targetSeparatorRow = -1;
    if (cursorIdx.isValid() && m->rowAt(cursorIdx.row()).kind == RowKindSeparator)
        targetSeparatorRow = cursorIdx.row();

    QList<int> srcRows;
    {
        QSet<int> seen;
        for (const auto& idx : selectionModel()->selectedRows(ModColName)) {
            int r = idx.row();
            if (r < 0 || r >= count || seen.contains(r))
                continue;
            if (m->rowAt(r).kind == RowKindOverwrite)
                continue;
            if (r == targetSeparatorRow)
                continue;
            seen.insert(r);
            srcRows.append(r);
        }
        std::sort(srcRows.begin(), srcRows.end());
    }
    if (srcRows.isEmpty()) {
        event->ignore();
        return;
    }

    int destRow = dropTargetRow(event);
    int overwriteRow = m->overwriteRow();
    int floor = (overwriteRow >= 0) ? overwriteRow : count;

    if (destRow < 0)
        destRow = 0;
    if (destRow >= count)
        destRow = floor;
    if (m->rowAt(destRow).kind == RowKindOverwrite)
        destRow = floor;

    int landingRow = m->moveRowsTo(srcRows, destRow);
    if (landingRow < 0) {
        event->ignore();
        return;
    }

    m->recalculatePriorities();
    m_owner->persistRowOrder();

    auto* sel = selectionModel();
    sel->clearSelection();
    QItemSelection range;
    int lastCol = m->columnCount() - 1;
    for (int i = 0; i < srcRows.size(); ++i) {
        QModelIndex top = m->index(landingRow + i, 0);
        QModelIndex bot = m->index(landingRow + i, lastCol);
        range.select(top, bot);
    }
    sel->select(range, QItemSelectionModel::ClearAndSelect | QItemSelectionModel::Rows);

    event->setDropAction(Qt::CopyAction);
    event->accept();
}

void ModListTreeView::startDrag(Qt::DropActions supportedActions)
{
    m_owner->beginInteraction();
    QTreeView::startDrag(supportedActions);
    m_owner->endInteraction();
}

QStringList ModListWidget::defaultCategories()
{
    return {
        "Animations", "Armour", "Audio", "Body, Face, & Hair", "Bugfixes",
        "Character Presets", "Cheats", "Cities", "Clothing", "Collectables",
        "Combat", "Companions", "Crafting", "Creatures, Mounts, & Vehicles",
        "Environment", "Factions", "Gameplay", "Immersion", "Items",
        "Landscape Changes", "Locations", "Magic", "Mercantile",
        "Modders Resources", "Models & Textures", "NPCs", "Overhauls",
        "Patches", "Perks", "Player Homes", "Poses", "Radio", "Settlements",
        "Shouts", "Skills & Levelling", "Utilities", "Weapons", "Weather & Lighting",
    };
}

ModListWidget::ModListWidget(GrpcClient* grpc, InstallController* installs, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
    , m_installs(installs)
    , m_saveQueue(new ModListSaveQueue(grpc, this))
{
    auto* layout = new QVBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);

    auto* headerRow = new QHBoxLayout;
    auto* titleLabel = new QLabel("Mod List");
    titleLabel->setStyleSheet("font-weight: bold;");
    headerRow->addWidget(titleLabel);
    m_profileStateLabel = new QLabel;
    m_profileStateLabel->setObjectName("hintLabel");
    m_profileStateLabel->hide();
    headerRow->addWidget(m_profileStateLabel);
    m_profileRetryButton = new QPushButton("Retry");
    m_profileRetryButton->hide();
    headerRow->addWidget(m_profileRetryButton);
    connect(m_profileRetryButton, &QPushButton::clicked, this, &ModListWidget::requestProfileModList);
    headerRow->addStretch();

    m_visualCheck = new QCheckBox("Separator View");
    m_visualCheck->setToolTip(
        "Separator View: separators are shown and dragging rearranges the\n"
        "mod list for display only. Turn off to edit the real load order\n"
        "(lower = overrides higher) — separators disappear while off.\n"
        "State is remembered per profile.");
    headerRow->addWidget(m_visualCheck);
    layout->addLayout(headerRow);

    connect(m_visualCheck, &QCheckBox::toggled, this, &ModListWidget::onVisualToggled);

    m_model = new ModListModel(this);

    m_view = new ModListTreeView(this);
    setFocusProxy(m_view);
    m_view->setModel(m_model);
    m_view->setItemDelegate(new ModListRowDelegate(m_view));
    m_view->setRootIsDecorated(false);
    m_view->setSelectionMode(QAbstractItemView::ExtendedSelection);
    m_view->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_view->setDragEnabled(true);
    m_view->setAcceptDrops(true);
    m_view->setDropIndicatorShown(true);
    m_view->setDragDropMode(QAbstractItemView::DragDrop);
    m_view->setDefaultDropAction(Qt::MoveAction);
    m_view->setContextMenuPolicy(Qt::CustomContextMenu);

    m_view->setSortingEnabled(false);
    m_view->header()->setSectionsClickable(true);
    m_view->header()->setSortIndicatorShown(true);
    m_view->header()->setSortIndicator(ModColPriority, Qt::AscendingOrder);

    m_view->header()->setSectionResizeMode(ModColPriority, QHeaderView::ResizeToContents);
    m_view->header()->setSectionResizeMode(ModColConflicts, QHeaderView::ResizeToContents);
    m_view->header()->setSectionResizeMode(ModColName, QHeaderView::Stretch);
    m_view->header()->setSectionResizeMode(ModColCategory, QHeaderView::ResizeToContents);
    m_view->header()->setSectionResizeMode(ModColVersion, QHeaderView::ResizeToContents);

    connect(ThemeManager::instance(), &ThemeManager::themeChanged,
            this, [this](const Palette&) { m_view->viewport()->update(); });

    connect(m_view->header(), &QHeaderView::sectionClicked, this, &ModListWidget::onHeaderClicked);
    connect(m_grpc, &GrpcClient::conflictsReceived, this, &ModListWidget::onConflictsReceived);
    connect(m_model, &ModListModel::dataChanged, this, &ModListWidget::onModelDataChanged);
    connect(m_view, &QTreeView::doubleClicked, this, &ModListWidget::onItemDoubleClicked);
    connect(m_view, &QTreeView::customContextMenuRequested, this, &ModListWidget::onContextMenu);
    connect(m_view->selectionModel(), &QItemSelectionModel::selectionChanged,
            this, &ModListWidget::onSelectionChanged);
    connect(m_grpc, &GrpcClient::modListRequestReceived, this, &ModListWidget::onProfileModListReceived);
    connect(m_grpc, &GrpcClient::modListRequestFailed, this, &ModListWidget::onProfileModListFailed);
    connect(m_saveQueue, &ModListSaveQueue::saveSucceeded, this, &ModListWidget::onModListSaved);
    connect(m_saveQueue, &ModListSaveQueue::saveFailed, this, &ModListWidget::onModListSaveFailed);
    connect(m_saveQueue, &ModListSaveQueue::drained, this, &ModListWidget::onModListSavesDrained);
    connect(m_grpc, &GrpcClient::connected, this, &ModListWidget::requestProfileModList);
    connect(m_installs, &InstallController::reinstallSucceeded, this,
            [this](quint64 id, const GrpcReinstallResult& result) {
        if (m_modAction && m_modAction->requestId == id)
            onModReinstalled(id, m_modAction->context.gameId, m_modAction->folder, result);
    });
    connect(m_installs, &InstallController::reinstallFailed, this, &ModListWidget::onReinstallFailed);
    connect(m_installs, &InstallController::cancelled, this, &ModListWidget::onReinstallCancelled);
    connect(m_installs, &InstallController::outcomeUnknown, this, &ModListWidget::onReinstallUnknown);
    connect(m_installs, &InstallController::reconciling, this, [this](quint64 id) {
        if (!m_modAction || m_modAction->kind != ModActionKind::Reinstall || m_modAction->requestId != id)
            return;
        if (m_reinstallProgress) {
            m_reinstallProgress->setCancelButton(nullptr);
            m_reinstallProgress->setLabelText("Checking whether this mod was reinstalled…");
        }
        if (m_bulkReinstall && m_bulkReinstall->progress) {
            m_bulkReinstall->progress->setCancelButtonText("Stop After This Mod");
            m_bulkReinstall->progress->setLabelText("Checking whether this mod was reinstalled…");
        }
    });
    connect(m_grpc, &GrpcClient::modUninstalled, this, &ModListWidget::onModUninstalled);
    connect(m_grpc, &GrpcClient::modRenamed, this, &ModListWidget::onModRenamed);
    connect(m_grpc, &GrpcClient::modActionFailed, this, &ModListWidget::onModActionFailed);
    connect(m_grpc, &GrpcClient::workersStopped, this, &ModListWidget::onModActionWorkersStopped);

    layout->addWidget(m_view);

    auto* footerRow = new QHBoxLayout;
    footerRow->addStretch();
    m_addSeparatorBtn = new QPushButton("+ Separator");
    m_addSeparatorBtn->setFlat(true);
    m_addSeparatorBtn->setToolTip(
        "Add a new separator above the Overwrite row.\n"
        "Hold Shift to add at the top of the list instead.\n"
        "(Existing separators can be repositioned via right-click → Move to Top/Bottom.)");
    footerRow->addWidget(m_addSeparatorBtn);
    layout->addLayout(footerRow);
    connect(m_addSeparatorBtn, &QPushButton::clicked, this, &ModListWidget::onAddSeparatorClicked);

    m_profileRetryTimer = new QTimer(this);
    m_profileRetryTimer->setSingleShot(true);
    connect(m_profileRetryTimer, &QTimer::timeout, this, &ModListWidget::onProfileRetryTimeout);

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

bool ModListWidget::modListSavesIdle() const
{
    return m_saveQueue->isIdle();
}

void ModListWidget::loadForGame(const GameInfo& game)
{
    loadForGame(game, "Default");
}

void ModListWidget::loadForGame(const GameInfo& game, const QString& profileName)
{
    const QString gameId = game.detected ? game.shortName : QString();
    const QString newProfile = game.detected ? profileName : QString();
    const QString modsDir = game.detected ? GameInfo::modsDirPathFor(gameId) : QString();
    if (m_bulkReinstall && m_bulkReinstall->progress) {
        const bool active = m_bulkReinstall->context.gameId == gameId
            && m_bulkReinstall->context.profileName == newProfile
            && m_bulkReinstall->context.modsDir == modsDir;
        m_bulkReinstall->progress->setVisible(active);
    }
    if (gameId == m_gameId && newProfile == m_profileName && modsDir == m_modsDir && isInteracting()) {
        m_reloadPending = true;
        return;
    }
    if (gameId != m_gameId || newProfile != m_profileName || modsDir != m_modsDir) {
        m_saveQueue->setContext(gameId, newProfile, modsDir);
        m_gameId = gameId;
        m_profileName = newProfile;
        m_modsDir = modsDir;
        m_profileRetryTimer->stop();
        m_profileListRequestId = 0;
        m_profileListPending = false;
        m_enableSaves.clear();
        m_restoringSavedProfile = false;
        m_savedProfileRestored = false;
        clearDependencyReport();
        dropProfileAdoption();
    }
    ++m_scanGeneration;
    m_scanFirstPending = game.detected;
    m_scanFolderPending = false;
    m_scanCatalogPending = false;
    if (m_scanAdoptionPending)
        emit modListAdoptionDeferred(m_scanAdoptionId);
    m_scanAdoptionPending.reset();
    m_updatingModel = true;
    m_model->clear();
    m_mods.clear();
    m_activeGame = game;
    m_sortColumn = ModColPriority;
    m_sortOrder = Qt::AscendingOrder;
    m_view->header()->setSortIndicator(ModColPriority, Qt::AscendingOrder);
    m_updatingModel = false;

    if (!game.detected) {
        m_reloadPending = false;
        updateEditLock();
        m_view->hide();
        m_placeholder->show();
        return;
    }

    updateEditLock();

    m_placeholder->hide();
    m_view->show();

    scanModsFolder();
    if (!m_pendingBulkSummaries.empty())
        QTimer::singleShot(0, this, &ModListWidget::showPendingBulkSummaries);
}

void ModListWidget::scanModsFolder()
{
    if (isInteracting()) {
        m_reloadPending = true;
        return;
    }
    m_reloadPending = false;
    ++m_editSerial;
    requestScan(ScanPurpose::Folder);
}

void ModListWidget::requestScan(ScanPurpose purpose)
{
    ++m_scanGeneration;
    if (purpose == ScanPurpose::Folder)
        m_scanFolderPending = true;
    else if (purpose == ScanPurpose::Catalog)
        m_scanCatalogPending = true;
    startScan();
    updateEditLock();
}

void ModListWidget::startScan()
{
    if (m_scanRunning || m_modsDir.isEmpty()
        || (!m_scanFolderPending && !m_scanCatalogPending && !m_scanAdoptionPending))
        return;
    m_scanRunning = true;
    const ScanTag tag{actionContext(), m_scanGeneration};
    const QString modsDir = tag.context.modsDir;
    QPointer<ModListWidget> receiver(this);
    QThreadPool::globalInstance()->start([modsDir, tag, receiver] {
        std::vector<ModMetadata> scanned = ModCatalog::scan(modsDir);
        if (!receiver)
            return;
        QMetaObject::invokeMethod(receiver.data(), [receiver, tag, scanned = std::move(scanned)]() mutable {
            if (receiver)
                receiver->onScanFinished(tag, std::move(scanned));
        }, Qt::QueuedConnection);
    });
}

void ModListWidget::onScanFinished(const ScanTag& tag, std::vector<ModMetadata> scanned)
{
    if (tag.generation == m_scanGeneration && matchesContext(tag.context)) {
        const bool folder = m_scanFolderPending;
        const bool catalog = m_scanCatalogPending;
        auto adoption = std::move(m_scanAdoptionPending);
        const quint64 adoptionSerial = m_scanAdoptionSerial;
        const quint64 adoptionId = m_scanAdoptionId;
        m_scanFolderPending = false;
        m_scanCatalogPending = false;
        m_scanAdoptionPending.reset();
        if (isInteracting()) {
            if (folder || catalog)
                m_reloadPending = true;
            if (adoption) {
                m_profileListPending = true;
                emit modListAdoptionDeferred(adoptionId);
            }
        } else {
            if (folder)
                finishFolderScan(scanned, tag);
            else if (catalog)
                finishCatalogScan(scanned);
            if (adoption) {
                if (folder || catalog || tag.generation != m_scanGeneration || !matchesContext(tag.context)) {
                    emit modListAdoptionDeferred(adoptionId);
                } else if (!m_saveQueue->isIdle()) {
                    m_profileListPending = true;
                    emit modListAdoptionDeferred(adoptionId);
                } else if (m_editSerial != adoptionSerial) {
                    requestProfileModList();
                    emit modListAdoptionDeferred(adoptionId);
                } else {
                    finishAdoption(*adoption, std::move(scanned), adoptionId);
                }
            }
        }
    }
    m_scanRunning = false;
    startScan();
}

void ModListWidget::finishFolderScan(std::vector<ModMetadata> scanned, const ScanTag& tag)
{
    const ActionContext context = actionContext();
    m_separators.clear();
    m_mods = scanCatalog(std::move(scanned));
    if (!m_gameId.isEmpty() && !m_profileName.isEmpty()) {
        std::vector<GrpcSeparator> seps;
        bool viewEnabled = false;
        QString err;
        const bool loaded = m_grpc->listSeparators(context.gameId, context.profileName, seps, viewEnabled, err);
        if (!matchesContext(context) || tag.generation != m_scanGeneration)
            return;
        if (isInteracting()) {
            m_reloadPending = true;
            return;
        }
        if (loaded) {
            for (const auto& s : seps) {
                SeparatorDef d;
                d.name = s.name;
                d.visualIndex = s.visualIndex;
                d.collapsed = s.collapsed;
                m_separators.push_back(d);
            }
            m_visualMode = m_collapsedSeparatorView ? true : viewEnabled;
            QSignalBlocker block(m_visualCheck);
            m_visualCheck->setChecked(m_visualMode);
            m_visualCheck->setEnabled(!m_collapsedSeparatorView);
        }
    }
    m_scanFirstPending = false;
    updateEditLock();
    rebuildView();
    requestProfileModList();
    if (!m_gameId.isEmpty() && !m_profileName.isEmpty())
        m_grpc->getConflicts(m_gameId, m_profileName);
}

void ModListWidget::onConflictsReceived(const std::vector<GrpcFileConflict>& conflicts)
{
    m_conflicts = conflicts;

    QHash<QString, int> winCounts;
    QHash<QString, int> loseCounts;
    for (const auto& c : conflicts) {
        winCounts[c.winningMod]++;
        for (const auto& l : c.losingMods)
            loseCounts[l]++;
    }

    m_model->applyConflictCounts(winCounts, loseCounts);
    updateConflictTints();
}

void ModListWidget::onSelectionChanged()
{
    updateConflictTints();
}

// Tints rows red/green to show what the single-selected mod overwrites or is overwritten by.
void ModListWidget::updateConflictTints()
{
    auto rows = m_view->selectionModel()->selectedRows();
    if (rows.size() != 1) {
        m_model->clearTints();
        return;
    }

    int selRow = rows.first().row();
    const ModListRow& sel = m_model->rowAt(selRow);
    if (sel.kind != RowKindMod) {
        m_model->clearTints();
        return;
    }
    QString selectedMod = sel.name;

    QSet<QString> loserOf;
    QSet<QString> winnerOver;
    for (const auto& c : m_conflicts) {
        if (c.winningMod == selectedMod) {
            for (const auto& l : c.losingMods)
                loserOf.insert(l);
        } else {
            for (const auto& l : c.losingMods) {
                if (l == selectedMod) {
                    winnerOver.insert(c.winningMod);
                    break;
                }
            }
        }
    }

    m_model->applySelectionTints(selRow, loserOf, winnerOver);
}

// Pops a dialog listing every file-level conflict involving modName, partitioned into wins/losses.
void ModListWidget::showConflictDetailsForMod(const QString& modName)
{
    QList<QPair<QString, QString>> winsOver;
    QList<QPair<QString, QString>> overwrittenBy;
    for (const auto& c : m_conflicts) {
        if (c.winningMod == modName) {
            for (const auto& l : c.losingMods)
                winsOver.append({c.virtualPath, l});
        } else {
            for (const auto& l : c.losingMods) {
                if (l == modName) {
                    overwrittenBy.append({c.virtualPath, c.winningMod});
                    break;
                }
            }
        }
    }

    auto* dlg = new QDialog(this);
    dlg->setAttribute(Qt::WA_DeleteOnClose);
    dlg->setWindowTitle(QString("Conflicts: %1").arg(modName));
    auto* layout = new QVBoxLayout(dlg);

    auto buildSection = [&](const QString& heading, const QColor& accent,
                            const QList<QPair<QString, QString>>& rows,
                            const QString& emptyText, const QString& otherCol) {
        auto* lbl = new QLabel(QString("<b><span style='color:%2'>%1</span></b>")
                                   .arg(heading.toHtmlEscaped(), accent.name()));
        lbl->setTextFormat(Qt::RichText);
        layout->addWidget(lbl);
        if (rows.isEmpty()) {
            auto* none = new QLabel(QString("<i>%1</i>").arg(emptyText.toHtmlEscaped()));
            none->setTextFormat(Qt::RichText);
            layout->addWidget(none);
            return;
        }
        auto* tree = new QTreeWidget;
        tree->setHeaderLabels({"File", otherCol});
        tree->setRootIsDecorated(false);
        tree->setAlternatingRowColors(true);
        tree->setUniformRowHeights(true);
        for (const auto& r : rows) {
            auto* item = new QTreeWidgetItem;
            item->setText(0, r.first);
            item->setText(1, r.second);
            tree->addTopLevelItem(item);
        }
        tree->resizeColumnToContents(0);
        layout->addWidget(tree, 1);
    };

    buildSection(QString("Overwrites %1 file(s):").arg(winsOver.size()),
                 ThemeManager::currentPalette().successFg, winsOver,
                 "This mod doesn't overwrite any files.", "Loser");
    buildSection(QString("Overwritten in %1 file(s):").arg(overwrittenBy.size()),
                 ThemeManager::currentPalette().errorFg, overwrittenBy,
                 "This mod isn't overwritten by any other mod.", "Winner");

    auto* close = new QPushButton("Close");
    connect(close, &QPushButton::clicked, dlg, &QDialog::accept);
    close->setDefault(true);
    layout->addWidget(close, 0, Qt::AlignRight);
    fitToScreen(dlg, QSize(720, 480));
    dlg->show();
}

// Sends a checkbox toggle with the profile's full mod list to the daemon.
void ModListWidget::onModelDataChanged(const QModelIndex& topLeft, const QModelIndex&,
                                       const QList<int>& roles)
{
    if (m_updatingModel)
        return;

    if ((roles.contains(Qt::CheckStateRole) || roles.isEmpty()) && topLeft.column() == ModColName) {
        int row = topLeft.row();
        const ModListRow& r = m_model->rowAt(row);
        if (r.kind != RowKindMod) return;
        int modIdx = r.modIndex;
        if (modIdx < 0 || modIdx >= int(m_mods.size())) return;

        if (editsBlocked()) {
            m_updatingModel = true;
            m_model->setData(topLeft, m_mods[modIdx].enabled ? Qt::Checked : Qt::Unchecked, Qt::CheckStateRole);
            m_updatingModel = false;
            return;
        }

        bool enabled = r.checked;
        m_mods[modIdx].enabled = enabled;
        if (m_profileAdopted)
            m_profileFlags.insert(m_mods[modIdx].folder, enabled);

        if (!m_gameId.isEmpty() && !m_profileName.isEmpty())
            submitModList(toggleEntries());

        emit modToggled();
    }
}

std::vector<GrpcModListEntry> ModListWidget::toggleEntries() const
{
    std::vector<GrpcModListEntry> entries;
    if (!m_visualMode) {
        entries.reserve(m_mods.size());
        std::vector<int> idx(m_mods.size());
        for (size_t i = 0; i < m_mods.size(); ++i) idx[i] = int(i);
        std::stable_sort(idx.begin(), idx.end(), [this](int a, int b) {
            quint64 ka = parseHexIndex(m_mods[a].trueIndex);
            quint64 kb = parseHexIndex(m_mods[b].trueIndex);
            if (ka == 0 && kb == 0) return a < b;
            if (ka == 0) return false;
            if (kb == 0) return true;
            return ka < kb;
        });
        for (int i : idx) {
            GrpcModListEntry e;
            e.modName = m_mods[i].folder;
            e.enabled = m_mods[i].enabled;
            e.priority = int(entries.size());
            entries.push_back(std::move(e));
        }
        return entries;
    }
    std::vector<int> idx(m_mods.size());
    for (size_t i = 0; i < m_mods.size(); ++i) idx[i] = int(i);
    std::stable_sort(idx.begin(), idx.end(), [this](int a, int b) {
        quint64 ka = parseHexIndex(m_mods[a].trueIndex);
        quint64 kb = parseHexIndex(m_mods[b].trueIndex);
        if (ka == 0 && kb == 0) return a < b;
        if (ka == 0) return false;
        if (kb == 0) return true;
        return ka < kb;
    });
    entries.reserve(idx.size());
    int p = 0;
    for (int i : idx) {
        GrpcModListEntry e;
        e.modName = m_mods[i].folder;
        e.enabled = m_mods[i].enabled;
        e.priority = p++;
        entries.push_back(std::move(e));
    }
    return entries;
}

ModListWidget::ActionContext ModListWidget::actionContext() const
{
    return {m_gameId, m_profileName, m_modsDir};
}

bool ModListWidget::matchesContext(const ActionContext& context) const
{
    return context.gameId == m_gameId && context.profileName == m_profileName
        && context.modsDir == m_modsDir;
}

int ModListWidget::modIndexForFolder(const QString& folder) const
{
    for (int i = 0; i < int(m_mods.size()); ++i) {
        if (m_mods[i].folder == folder)
            return i;
    }
    return -1;
}

int ModListWidget::separatorIndexForName(const QString& name) const
{
    for (int i = 0; i < int(m_separators.size()); ++i) {
        if (m_separators[i].name == name)
            return i;
    }
    return -1;
}

QString ModListWidget::metadataPathForFolder(const QString& folder) const
{
    if (folder.isEmpty() || folder == "." || folder == ".." || folder.contains('/') || folder.contains('\\'))
        return {};
    const QFileInfo dir(m_modsDir + "/" + folder);
    if (!dir.isDir() || dir.isSymLink())
        return {};
    return m_modsDir + "/" + folder + "/metadata.yaml";
}

int ModListWidget::availableModIndex(const ActionContext& context, const QString& folder)
{
    const int index = matchesContext(context) ? modIndexForFolder(folder) : -1;
    if (index >= 0 && !metadataPathForFolder(folder).isEmpty())
        return index;
    QMessageBox::information(this, "Mod unavailable",
                             "This mod is no longer available. Refresh the list and try again.");
    return -1;
}

int ModListWidget::availableSeparatorIndex(const ActionContext& context, const QString& name)
{
    const int index = matchesContext(context) ? separatorIndexForName(name) : -1;
    if (index >= 0) {
        std::vector<GrpcSeparator> separators;
        bool viewEnabled = false;
        QString error;
        const bool loaded = m_grpc->listSeparators(context.gameId, context.profileName,
                                                    separators, viewEnabled, error);
        if (loaded && matchesContext(context) && std::any_of(separators.begin(), separators.end(),
            [&name](const GrpcSeparator& separator) { return separator.name == name; }))
            return separatorIndexForName(name);
    }
    QMessageBox::information(this, "Separator unavailable",
                             "This separator is no longer available. Refresh the list and try again.");
    return -1;
}

bool ModListWidget::containsMod(const QString& folder) const
{
    return modIndexForFolder(folder) >= 0;
}

void ModListWidget::reloadMods()
{
    if (m_gameId.isEmpty() || m_modsDir.isEmpty())
        return;
    if (isInteracting()) {
        m_reloadPending = true;
        return;
    }
    rescanCatalog();
}

void ModListWidget::reloadAfterFailedSave(const GameInfo& game, const QString& profileName)
{
    if (game.shortName != m_gameId || profileName != m_profileName) {
        loadForGame(game, profileName);
        return;
    }
    dropProfileAdoption();
    reloadMods();
}

void ModListWidget::rescanCatalog()
{
    if (isInteracting()) {
        m_reloadPending = true;
        return;
    }
    ++m_editSerial;
    m_reloadPending = false;
    requestScan(ScanPurpose::Catalog);
}

void ModListWidget::finishCatalogScan(std::vector<ModMetadata> scanned)
{
    m_mods = scanCatalog(std::move(scanned));
    m_scanFirstPending = false;
    updateEditLock();
    requestProfileModList();
    refreshView();
}

std::vector<ModMetadata> ModListWidget::scanCatalog(std::vector<ModMetadata> scanned) const
{
    if (!m_profileAdopted) {
        for (auto& meta : scanned) {
            meta.enabled = false;
            meta.trueIndex.clear();
        }
        return scanned;
    }
    quint64 next = 1;
    for (auto it = m_profileOrder.cbegin(); it != m_profileOrder.cend(); ++it)
        next = std::max(next, it.value() + 1);
    std::vector<int> unknown;
    for (int i = 0; i < int(scanned.size()); ++i) {
        ModMetadata& meta = scanned[i];
        meta.enabled = m_profileFlags.value(meta.folder, false);
        const auto position = m_profileOrder.constFind(meta.folder);
        if (position == m_profileOrder.constEnd())
            unknown.push_back(i);
        else
            meta.trueIndex = formatHexIndex(position.value() * kTrueIndexStep);
    }
    std::stable_sort(unknown.begin(), unknown.end(), [&scanned](int a, int b) {
        const quint64 ka = parseHexIndex(scanned[a].trueIndex);
        const quint64 kb = parseHexIndex(scanned[b].trueIndex);
        if (ka == 0 || kb == 0)
            return ka != 0 && kb == 0;
        return ka < kb;
    });
    for (int i : unknown)
        scanned[i].trueIndex = formatHexIndex(next++ * kTrueIndexStep);
    return scanned;
}

void ModListWidget::noteSentModList(const std::vector<GrpcModListEntry>& entries)
{
    if (!m_profileAdopted)
        return;
    QHash<QString, quint64> order;
    quint64 next = 1;
    for (const auto& entry : entries) {
        if (!entry.modName.isEmpty() && !order.contains(entry.modName))
            order.insert(entry.modName, next++);
    }
    std::vector<std::pair<quint64, QString>> rest;
    for (auto it = m_profileOrder.cbegin(); it != m_profileOrder.cend(); ++it) {
        if (!order.contains(it.key()))
            rest.emplace_back(it.value(), it.key());
    }
    std::sort(rest.begin(), rest.end());
    for (const auto& [position, folder] : rest)
        order.insert(folder, next++);
    m_profileOrder = order;
    for (auto& meta : m_mods) {
        const auto position = m_profileOrder.constFind(meta.folder);
        if (position != m_profileOrder.constEnd())
            meta.trueIndex = formatHexIndex(position.value() * kTrueIndexStep);
    }
}

quint64 ModListWidget::submitModList(const std::vector<GrpcModListEntry>& entries)
{
    ++m_editSerial;
    noteSentModList(entries);
    m_savedProfileRestored = false;
    updateEditLock();
    return m_saveQueue->submit(entries);
}

void ModListWidget::requestProfileModList()
{
    m_profileRetryTimer->stop();
    m_profileRetryAttempts = 0;
    m_profileLoadFailed = false;
    m_profileLoadError.clear();
    sendProfileModListRequest();
    updateEditLock();
}

void ModListWidget::sendProfileModListRequest()
{
    if (m_gameId.isEmpty() || m_profileName.isEmpty())
        return;
    m_profileListPending = false;
    m_profileListSerial = m_editSerial;
    m_profileListRequestId = m_grpc->getModListTracked(m_gameId, m_profileName);
}

void ModListWidget::onProfileModListFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                                           const QString& error)
{
    if (requestId == 0 || requestId != m_profileListRequestId)
        return;
    m_profileListRequestId = 0;
    if (gameId != m_gameId || profileName != m_profileName)
        return;
    if (m_profileRetryAttempts < kProfileListRetries) {
        m_profileRetryTimer->start(kProfileListRetryBaseMs << m_profileRetryAttempts);
        ++m_profileRetryAttempts;
        return;
    }
    m_profileLoadFailed = true;
    m_profileLoadError = error;
    updateEditLock();
}

void ModListWidget::onProfileRetryTimeout()
{
    sendProfileModListRequest();
}

void ModListWidget::dropProfileAdoption()
{
    m_profileAdopted = false;
    m_profileFlags.clear();
    m_profileOrder.clear();
    m_profileLoadFailed = false;
    m_profileLoadError.clear();
    updateEditLock();
}

bool ModListWidget::editsBlocked() const
{
    return m_modActionInProgress || (!m_gameId.isEmpty()
        && (m_scanFirstPending || (!m_profileName.isEmpty() && !m_profileAdopted)));
}

void ModListWidget::updateEditLock()
{
    const bool blocked = editsBlocked();
    const bool failed = blocked && m_profileLoadFailed;
    m_model->setEditable(!blocked);
    m_addSeparatorBtn->setEnabled(!blocked);
    m_visualCheck->setEnabled(!m_modActionInProgress && !m_collapsedSeparatorView);
    if (m_modActionInProgress && m_modAction) {
        const ModAction& action = *m_modAction;
        if (!matchesContext(action.context)) {
            m_profileStateLabel->setText(QStringLiteral("Finishing another mod change…"));
        } else if (action.kind == ModActionKind::Reinstall) {
            m_profileStateLabel->setText(QStringLiteral("Reinstalling \"%1\"…").arg(action.name));
        } else if (action.kind == ModActionKind::Uninstall) {
            m_profileStateLabel->setText(QStringLiteral("Uninstalling \"%1\"…").arg(action.name));
        } else {
            m_profileStateLabel->setText(QStringLiteral("Renaming \"%1\"…").arg(action.name));
        }
        m_profileStateLabel->setToolTip(QString());
    } else if (m_scanFirstPending && !m_gameId.isEmpty()) {
        m_profileStateLabel->setText(QStringLiteral("Refreshing mods…"));
        m_profileStateLabel->setToolTip(QString());
    } else if (failed) {
        m_profileStateLabel->setText(m_restoringSavedProfile
            ? QStringLiteral("Couldn't reload the saved profile. Try again.")
            : QStringLiteral("Couldn't load this profile. Mod changes are disabled."));
        m_profileStateLabel->setToolTip(plainToolTip(errorSummary("load this profile", m_profileLoadError)));
    } else if (m_restoringSavedProfile) {
        m_profileStateLabel->setText(QStringLiteral("Couldn't save your mod choices. Reloading the saved profile…"));
        m_profileStateLabel->setToolTip(QString());
    } else if (m_savedProfileRestored) {
        m_profileStateLabel->setText(QStringLiteral("Saved mod choices restored."));
        m_profileStateLabel->setToolTip(QString());
    } else if (blocked) {
        m_profileStateLabel->setText(QStringLiteral("Loading profile…"));
        m_profileStateLabel->setToolTip(QStringLiteral("Checkboxes, drag-and-drop and separator moves are available "
                                                       "once the profile's mod list has loaded from the gorganizer "
                                                       "daemon."));
    }
    m_profileStateLabel->setVisible(blocked || m_restoringSavedProfile || m_savedProfileRestored);
    m_profileRetryButton->setVisible(failed && !m_scanFirstPending && !m_modActionInProgress);
}

void ModListWidget::onProfileModListReceived(quint64 requestId, const QString& gameId, const QString& profileName,
                                             const std::vector<GrpcModListEntry>& entries)
{
    if (requestId == 0 || requestId != m_profileListRequestId)
        return;
    m_profileListRequestId = 0;
    if (gameId != m_gameId || profileName != m_profileName)
        return;
    if (m_editSerial != m_profileListSerial) {
        requestProfileModList();
        return;
    }
    if (isInteracting() || !m_saveQueue->isIdle()) {
        m_profileListPending = true;
        return;
    }
    adoptModList(entries);
}

void ModListWidget::refreshView()
{
    rebuildView();
    if (!(m_sortColumn == ModColPriority && m_sortOrder == Qt::AscendingOrder))
        m_model->sortBy(m_sortColumn, m_sortOrder);
    onConflictsReceived(m_conflicts);
    if (!m_gameId.isEmpty() && !m_profileName.isEmpty())
        m_grpc->getConflicts(m_gameId, m_profileName);
}

quint64 ModListWidget::adoptModList(const std::vector<GrpcModListEntry>& entries)
{
    if (m_gameId.isEmpty() || m_modsDir.isEmpty() || isInteracting() || !m_saveQueue->isIdle())
        return 0;
    m_reloadPending = false;
    m_profileListPending = false;
    if (m_scanAdoptionPending)
        emit modListAdoptionDeferred(m_scanAdoptionId);
    m_scanAdoptionPending = entries;
    m_scanAdoptionSerial = m_editSerial;
    requestScan(ScanPurpose::Adoption);
    m_scanAdoptionId = m_scanGeneration;
    return m_scanAdoptionId;
}

bool ModListWidget::finishAdoption(const std::vector<GrpcModListEntry>& entries, std::vector<ModMetadata> scanned,
                                   quint64 adoptionId)
{
    if (isInteracting() || !m_saveQueue->isIdle()) {
        m_profileListPending = true;
        return false;
    }
    QHash<QString, int> position;
    for (int i = 0; i < int(entries.size()); ++i) {
        if (!position.contains(entries[i].modName))
            position.insert(entries[i].modName, i);
    }
    std::vector<int> absent;
    m_profileFlags.clear();
    m_profileOrder.clear();
    for (int i = 0; i < int(scanned.size()); ++i) {
        ModMetadata& meta = scanned[i];
        const auto it = position.constFind(meta.folder);
        if (it == position.constEnd()) {
            meta.enabled = false;
            absent.push_back(i);
        } else {
            meta.enabled = entries[it.value()].enabled;
            meta.trueIndex = formatHexIndex(quint64(it.value() + 1) * kTrueIndexStep);
        }
        m_profileFlags.insert(meta.folder, meta.enabled);
    }
    std::stable_sort(absent.begin(), absent.end(), [&scanned](int a, int b) {
        const quint64 ka = parseHexIndex(scanned[a].trueIndex);
        const quint64 kb = parseHexIndex(scanned[b].trueIndex);
        if (ka == 0 || kb == 0)
            return ka != 0 && kb == 0;
        return ka < kb;
    });
    quint64 next = quint64(entries.size()) + 1;
    for (int i : absent)
        scanned[i].trueIndex = formatHexIndex(next++ * kTrueIndexStep);
    for (const auto& meta : scanned)
        m_profileOrder.insert(meta.folder, parseHexIndex(meta.trueIndex) / kTrueIndexStep);
    m_profileAdopted = true;
    m_scanFirstPending = false;
    const bool unchanged = sameRows(m_mods, scanned);
    m_mods = std::move(scanned);
    if (!unchanged)
        refreshView();
    if (m_restoringSavedProfile) {
        m_restoringSavedProfile = false;
        m_savedProfileRestored = true;
    }
    updateEditLock();
    emit modListAdopted(adoptionId);
    emit modListReadyForEnable();
    return true;
}

void ModListWidget::applyEnabledFlags(const QStringList& folders, bool enabled)
{
    const QSet<QString> wanted(folders.begin(), folders.end());
    m_updatingModel = true;
    for (int i = 0; i < int(m_mods.size()); ++i) {
        ModMetadata& meta = m_mods[i];
        if (!wanted.contains(meta.folder) || meta.enabled == enabled)
            continue;
        meta.enabled = enabled;
        if (m_profileAdopted)
            m_profileFlags.insert(meta.folder, enabled);
        const int row = m_model->rowForModIndex(i);
        if (row >= 0)
            m_model->setData(m_model->index(row, ModColName), enabled ? Qt::Checked : Qt::Unchecked,
                             Qt::CheckStateRole);
    }
    m_updatingModel = false;
}

void ModListWidget::setSelectedModsEnabled(const ActionContext& context, const QStringList& folders, bool enabled)
{
    if (editsBlocked())
        return;
    for (const QString& folder : folders) {
        const int index = availableModIndex(context, folder);
        if (index < 0)
            return;
        if (m_model->rowForModIndex(index) < 0) {
            QMessageBox::information(this, "Mod unavailable",
                                     "This mod is no longer available. Refresh the list and try again.");
            return;
        }
    }
    for (const QString& folder : folders) {
        const int index = availableModIndex(context, folder);
        if (index < 0)
            return;
        const int row = m_model->rowForModIndex(index);
        if (row < 0) {
            QMessageBox::information(this, "Mod unavailable",
                                     "This mod is no longer available. Refresh the list and try again.");
            return;
        }
        m_model->setData(m_model->index(row, ModColName), enabled ? Qt::Checked : Qt::Unchecked,
                         Qt::CheckStateRole);
    }
}

bool ModListWidget::readyForDependencyEnable() const
{
    return m_profileAdopted && !editsBlocked() && !m_restoringSavedProfile && !isInteracting()
        && m_saveQueue->isIdle();
}

quint64 ModListWidget::enableModsInProfile(const std::vector<GrpcModListEntry>& authoritative, const QStringList& names,
                                           QStringList* changedOut)
{
    if (changedOut)
        changedOut->clear();
    if (!readyForDependencyEnable() || m_gameId.isEmpty() || m_profileName.isEmpty() || names.isEmpty())
        return 0;
    QSet<QString> wanted;
    for (const auto& name : names) {
        if (containsMod(name))
            wanted.insert(name);
    }
    std::vector<GrpcModListEntry> batch;
    QSet<QString> listed;
    QStringList changed;
    for (const auto& entry : authoritative) {
        if (entry.modName.isEmpty() || listed.contains(entry.modName))
            continue;
        listed.insert(entry.modName);
        GrpcModListEntry out;
        out.modName = entry.modName;
        out.enabled = entry.enabled;
        if (!entry.enabled && wanted.contains(entry.modName)) {
            out.enabled = true;
            changed.append(entry.modName);
        }
        out.priority = int(batch.size());
        batch.push_back(std::move(out));
    }
    for (const auto& name : names) {
        if (!wanted.contains(name) || listed.contains(name))
            continue;
        listed.insert(name);
        GrpcModListEntry out;
        out.modName = name;
        out.enabled = true;
        out.priority = int(batch.size());
        batch.push_back(std::move(out));
        changed.append(name);
    }
    if (changed.isEmpty())
        return 0;
    applyEnabledFlags(changed, true);
    const quint64 requestId = submitModList(batch);
    m_enableSaves.insert(requestId, EnableSave{m_gameId, m_profileName, m_modsDir, changed, m_editSerial});
    if (changedOut)
        *changedOut = changed;
    emit modToggled();
    return requestId;
}

void ModListWidget::onModListSaved(quint64 requestId)
{
    m_enableSaves.remove(requestId);
}

void ModListWidget::onModListSaveFailed(quint64 requestId)
{
    const auto it = m_enableSaves.constFind(requestId);
    if (it != m_enableSaves.constEnd()) {
        const EnableSave save = it.value();
        m_enableSaves.erase(it);
        if (save.gameId == m_gameId && save.profileName == m_profileName && save.modsDir == m_modsDir
            && m_editSerial == save.editSerial)
            applyEnabledFlags(save.folders, false);
    }
    if (requestId == 0)
        m_enableSaves.clear();
    m_restoringSavedProfile = true;
    m_savedProfileRestored = false;
    dropProfileAdoption();
    reloadMods();
}

void ModListWidget::onModListSavesDrained()
{
    emit modListSavesDrained();
    if (m_profileListPending && !isInteracting()) {
        requestProfileModList();
        return;
    }
    if (readyForDependencyEnable())
        emit modListReadyForEnable();
}

void ModListWidget::setDependencyReport(const GrpcModDependencyReport& report)
{
    m_dependencyReport = report;
    const QHash<QString, QString> names = dependencyNames(report);
    QHash<QString, ModDependencySummary> summaries;
    QHash<QString, QStringList> lines;
    for (const auto& component : report.components) {
        if (component.providerMod.isEmpty() || component.providerMod == QLatin1String(kOverwriteModName))
            continue;
        const QString label = component.name.isEmpty() ? component.folder : component.name;
        QStringList& modLines = lines[component.providerMod];
        ModDependencySummary& summary = summaries[component.providerMod];
        summary.severity = std::max(summary.severity, static_cast<int>(componentSeverity(component)));
        for (const auto& issue : component.issues)
            modLines.append(QStringLiteral("%1: %2").arg(label, issueDescription(issue, names)));
        if (component.failed && component.issues.empty())
            modLines.append(QStringLiteral("%1: SMAPI will not load it.").arg(label));
        if (!component.updateVersion.isEmpty()) {
            modLines.append(QStringLiteral("%1: version %2 is available.").arg(label, component.updateVersion));
            if (summary.updateVersion.isEmpty())
                summary.updateVersion = component.updateVersion;
            if (summary.updateUrl.isEmpty() && component.updateUrl.startsWith(QLatin1String("https://")))
                summary.updateUrl = component.updateUrl;
        }
    }
    for (auto it = summaries.begin(); it != summaries.end();) {
        const QStringList modLines = lines.value(it.key());
        if (modLines.isEmpty()) {
            it = summaries.erase(it);
            continue;
        }
        it->tooltip = plainToolTip(modLines.join(QLatin1Char('\n')));
        ++it;
    }
    m_model->setDependencySummaries(summaries);
}

void ModListWidget::clearDependencyReport()
{
    m_dependencyReport.reset();
    m_model->setDependencySummaries({});
}

void ModListWidget::beginInteraction()
{
    ++m_interactionDepth;
}

void ModListWidget::endInteraction()
{
    if (m_interactionDepth > 0)
        --m_interactionDepth;
    if (m_interactionDepth > 0)
        return;
    if (m_reloadPending) {
        if (m_model->rowCount() == 0 && !m_gameId.isEmpty())
            scanModsFolder();
        else if (!m_gameId.isEmpty())
            rescanCatalog();
        else
            m_reloadPending = false;
    } else if (m_profileListPending) {
        requestProfileModList();
    }
    emit interactionFinished();
}

void ModListWidget::addDependencyActions(QMenu& menu, const QString& folder)
{
    QStringList missingIds;
    QStringList enableNames;
    QString updateUrl;
    if (m_dependencyReport) {
        QHash<QString, const GrpcMissingDependency*> missingById;
        for (const auto& dep : m_dependencyReport->missing)
            missingById.insert(dep.uniqueId.toCaseFolded(), &dep);
        QSet<QString> seenIds;
        QSet<QString> seenNames;
        for (const auto& component : m_dependencyReport->components) {
            if (component.providerMod != folder)
                continue;
            if (updateUrl.isEmpty() && component.updateUrl.startsWith(QLatin1String("https://")))
                updateUrl = component.updateUrl;
            for (const auto& issue : component.issues) {
                const QString key = issue.targetId.toCaseFolded();
                const GrpcMissingDependency* dep = missingById.value(key);
                if (issue.kind == GrpcModIssueMissing && !seenIds.contains(key)
                    && (!dep || dep->disabledProviders.isEmpty())) {
                    seenIds.insert(key);
                    missingIds.append(issue.targetId);
                } else if (issue.kind == GrpcModIssueDisabled) {
                    const QStringList providers =
                        dep && !dep->disabledProviders.isEmpty() ? dep->disabledProviders : issue.providers;
                    if (!providers.isEmpty() && !seenNames.contains(providers.front())) {
                        seenNames.insert(providers.front());
                        enableNames.append(providers.front());
                    }
                }
            }
        }
    }

    const ActionContext context = actionContext();
    menu.addSeparator();
    auto* fetch = menu.addAction(QStringLiteral("Fetch Missing Dependencies…"));
    fetch->setEnabled(!missingIds.isEmpty());
    connect(fetch, &QAction::triggered, this, [this, context, folder, missingIds] {
        if (availableModIndex(context, folder) >= 0)
            emit dependencyFetchRequested(missingIds);
    });
    auto* enable = menu.addAction(QStringLiteral("Enable Required Dependencies…"));
    enable->setEnabled(!enableNames.isEmpty());
    connect(enable, &QAction::triggered, this, [this, context, folder, enableNames] {
        if (availableModIndex(context, folder) >= 0)
            emit dependencyEnableRequested(enableNames);
    });
    auto* update = menu.addAction(QStringLiteral("Open Update Page"));
    update->setEnabled(!updateUrl.isEmpty());
    if (!updateUrl.isEmpty())
        update->setToolTip(plainToolTip(updateUrl));
    connect(update, &QAction::triggered, this, [this, context, folder, updateUrl] {
        if (availableModIndex(context, folder) >= 0)
            openWebLink(this, updateUrl);
    });
}

void ModListWidget::onItemDoubleClicked(const QModelIndex& index)
{
    if (!index.isValid())
        return;

    const ModListRow r = m_model->rowAt(index.row());
    const ActionContext context = actionContext();
    if (r.kind == RowKindSeparator) {
        toggleCollapseAt(context, r.name);
        return;
    }

    if (editsBlocked() || index.column() != ModColCategory || r.kind != RowKindMod
        || modIndexForFolder(r.folder) < 0)
        return;
    const QString folder = r.folder;

    QDialog dlg(m_view);
    dlg.setWindowTitle("Set Category");
    auto* combo = new QComboBox(&dlg);
    combo->setEditable(true);
    combo->addItem("");
    combo->addItems(defaultCategories());
    combo->setCurrentText(r.category);

    auto* dlgLayout = new QVBoxLayout(&dlg);
    dlgLayout->addWidget(new QLabel("Select or type a category:"));
    dlgLayout->addWidget(combo);
    auto* okBtn = new QPushButton("OK");
    dlgLayout->addWidget(okBtn);
    connect(okBtn, &QPushButton::clicked, &dlg, &QDialog::accept);
    okBtn->setDefault(true);
    fitToScreen(&dlg, QSize(360, 160));

    beginInteraction();
    if (dlg.exec() == QDialog::Accepted)
        setCategoryForFolder(context, folder, combo->currentText());
    endInteraction();
}

void ModListWidget::onContextMenu(const QPoint& pos)
{
    beginInteraction();
    showContextMenu(pos);
    endInteraction();
}

bool ModListWidget::refuseModAction() const
{
    if (!m_modActionInProgress)
        return false;
    if (auto* mainWindow = qobject_cast<QMainWindow*>(window()))
        mainWindow->statusBar()->showMessage(QStringLiteral("Wait for the current change to finish."), 5000);
    return true;
}

void ModListWidget::startModAction(ModAction action)
{
    if (!m_bulkReinstall && refuseModAction())
        return;
    action.submittedWhileConnected = m_grpc->isConnected();
    if (action.kind == ModActionKind::Reinstall)
        action.requestId = m_installs->reinstall(action.context.gameId, action.folder);
    else if (action.kind == ModActionKind::Uninstall)
        action.requestId = m_grpc->uninstallModAsync(action.context.gameId, action.folder, false);
    else
        action.requestId = m_grpc->renameModAsync(action.context.gameId, action.folder, action.newName);
    m_modAction = std::move(action);
    m_modActionInProgress = true;
    updateEditLock();
    if (m_modAction->kind == ModActionKind::Reinstall && !m_bulkReinstall) {
        auto* progress = new QProgressDialog("Reinstalling this mod…", "Cancel Reinstall", 0, 0, this);
        progress->setWindowTitle("Reinstall Mod");
        progress->setWindowModality(Qt::NonModal);
        progress->setMinimumDuration(0);
        progress->setAutoClose(false);
        const quint64 id = m_modAction->requestId;
        connect(progress, &QProgressDialog::canceled, this, [this, id, progress] {
            m_installs->cancel(id);
            QTimer::singleShot(0, progress, [progress] {
                progress->setCancelButton(nullptr);
                progress->setLabelText("Cancelling… checking whether this mod was reinstalled.");
                progress->show();
            });
        });
        m_reinstallProgress = progress;
        progress->show();
    }
}

void ModListWidget::startBulkReinstall(const ActionContext& context, const QStringList& folders,
                                       const QStringList& names)
{
    if (refuseModAction())
        return;
    m_bulkReinstall.emplace();
    m_bulkReinstall->context = context;
    m_bulkReinstall->folders = folders;
    m_bulkReinstall->names = names;
    m_bulkReinstall->total = folders.size();
    auto* progress = new QProgressDialog(this);
    progress->setWindowTitle(QStringLiteral("Reinstall Mods"));
    progress->setLabelText(QStringLiteral("Reinstalling 1 of %1…").arg(folders.size()));
    progress->setCancelButtonText(QStringLiteral("Cancel Current and Stop"));
    progress->setRange(0, folders.size());
    progress->setValue(0);
    progress->setMinimumDuration(0);
    progress->setAutoClose(false);
    progress->setAutoReset(false);
    progress->setWindowModality(Qt::NonModal);
    connect(progress, &QProgressDialog::canceled, this, [this] {
        if (!m_bulkReinstall)
            return;
        m_bulkReinstall->stopRequested = true;
        m_bulkReinstall->folders.resize(m_bulkReinstall->next);
        if (m_modAction && m_modAction->kind == ModActionKind::Reinstall)
            m_installs->cancel(m_modAction->requestId);
        QTimer::singleShot(0, this, [this] {
            if (!m_bulkReinstall || !m_modAction || !matchesContext(m_bulkReinstall->context)
                || !m_bulkReinstall->progress)
                return;
            m_bulkReinstall->progress->setCancelButton(nullptr);
            m_bulkReinstall->progress->show();
        });
    });
    m_bulkReinstall->progress = progress;
    m_modActionInProgress = true;
    progress->show();
    startNextBulkReinstall();
}

void ModListWidget::startNextBulkReinstall()
{
    if (!m_bulkReinstall)
        return;
    auto& bulk = *m_bulkReinstall;
    while (bulk.next < bulk.folders.size()) {
        const int index = bulk.next++;
        const QString& folder = bulk.folders[index];
        const QString path = bulk.context.modsDir + QLatin1Char('/') + folder;
        const QFileInfo dir(path);
        if (!dir.isDir() || dir.isSymLink()) {
            ++bulk.failed;
            bulk.errors.append(QStringLiteral("• %1: This mod is no longer available.").arg(bulk.names[index]));
            if (bulk.progress)
                bulk.progress->setValue(bulk.next);
            continue;
        }
        if (bulk.progress) {
            bulk.progress->setCancelButtonText("Cancel Current and Stop");
            bulk.progress->setLabelText(QStringLiteral("Reinstalling %1 of %2…")
                                            .arg(bulk.next).arg(bulk.total));
        }
        startModAction(ModAction{ModActionKind::Reinstall, bulk.context, folder, bulk.names[index]});
        return;
    }
    finishBulkReinstall();
}

void ModListWidget::finishBulkReinstall()
{
    if (!m_bulkReinstall)
        return;
    BulkReinstall bulk = *m_bulkReinstall;
    m_bulkReinstall.reset();
    m_modAction.reset();
    m_modActionInProgress = false;
    if (bulk.progress) {
        bulk.progress->hide();
        bulk.progress->deleteLater();
        bulk.progress = nullptr;
    }
    updateEditLock();
    if (!matchesContext(bulk.context)) {
        m_pendingBulkSummaries.push_back(bulk);
        return;
    }
    if (bulk.completed > 0) {
        reloadMods();
        emit modsEdited();
    }
    showBulkSummary(bulk);
}

void ModListWidget::showPendingBulkSummaries()
{
    for (;;) {
        const auto it = std::find_if(m_pendingBulkSummaries.begin(), m_pendingBulkSummaries.end(),
            [this](const BulkReinstall& bulk) { return matchesContext(bulk.context); });
        if (it == m_pendingBulkSummaries.end())
            return;
        const BulkReinstall bulk = *it;
        m_pendingBulkSummaries.erase(it);
        if (bulk.completed > 0)
            emit modsEdited();
        showBulkSummary(bulk);
    }
}

void ModListWidget::showBulkSummary(const BulkReinstall& bulk)
{
    QString summary = QStringLiteral("Reinstalled %1 mods; %2 failed; %3 could not be confirmed.")
                          .arg(bulk.completed).arg(bulk.failed).arg(bulk.unknown);
    if (bulk.stopRequested)
        summary += QStringLiteral("\nStopped. %1 mods were not attempted.").arg(bulk.total - bulk.next);
    if (bulk.failed > 0 || bulk.unknown > 0 || !bulk.notices.isEmpty()) {
        QMessageBox box(this);
        box.setIcon(bulk.failed > 0 || bulk.unknown > 0 ? QMessageBox::Warning : QMessageBox::Information);
        const QString title = bulk.failed > 0 || bulk.unknown > 0 ? QStringLiteral("Bulk Reinstall — Partial")
            : QStringLiteral("Bulk Reinstall Complete");
        box.setWindowTitle(title);
        box.setTextFormat(Qt::PlainText);
        box.setText(summary);
        if (bulk.unknown > 0)
            box.setInformativeText(QStringLiteral("Check Mods and Downloads before trying again. Show details for each mod."));
        else if (!bulk.notices.isEmpty())
            box.setInformativeText(QStringLiteral("Some source archives were missing. Show details for each mod."));
        box.setStandardButtons(QMessageBox::Ok);
        attachErrorDetails(&box, title, QStringLiteral("reinstall these mods"),
                           (bulk.errors + bulk.notices).join(QLatin1Char('\n')));
        box.exec();
    } else {
        dialogs::info(this, QStringLiteral("Bulk Reinstall Complete"), summary);
    }
}

bool ModListWidget::matchesModAction(quint64 requestId, const QString& gameId, const QString& modName,
                                     ModActionKind kind) const
{
    return m_modAction && m_modAction->requestId == requestId && m_modAction->context.gameId == gameId
        && m_modAction->folder == modName && m_modAction->kind == kind;
}

void ModListWidget::finishModAction(bool changed)
{
    const ActionContext context = m_modAction->context;
    m_modAction.reset();
    if (m_reinstallProgress) {
        m_reinstallProgress->hide();
        m_reinstallProgress->deleteLater();
        m_reinstallProgress = nullptr;
    }
    if (m_bulkReinstall) {
        if (m_bulkReinstall->progress)
            m_bulkReinstall->progress->setValue(m_bulkReinstall->next);
        startNextBulkReinstall();
        return;
    }
    m_modActionInProgress = false;
    updateEditLock();
    if (changed && matchesContext(context)) {
        reloadMods();
        emit modsEdited();
    }
}

void ModListWidget::onModReinstalled(quint64 requestId, const QString& gameId, const QString& modName,
                                     const GrpcReinstallResult& result)
{
    if (!matchesModAction(requestId, gameId, modName, ModActionKind::Reinstall))
        return;
    const bool current = matchesContext(m_modAction->context);
    if (m_bulkReinstall) {
        ++m_bulkReinstall->completed;
        if (result.archivesSkipped > 0) {
            m_bulkReinstall->notices.append(QStringLiteral("• %1: Replayed %2, skipped %3 missing archives. "
                                                            "%4 files total.")
                .arg(m_modAction->name).arg(result.archivesReplayed)
                .arg(result.archivesSkipped).arg(result.fileCount));
        }
        finishModAction(true);
        return;
    }
    finishModAction(true);
    if (current && result.archivesSkipped > 0) {
        dialogs::info(this, QStringLiteral("Reinstall Complete"),
                      QStringLiteral("Replayed %1, skipped %2 (missing archive). %3 files total.")
                          .arg(result.archivesReplayed).arg(result.archivesSkipped).arg(result.fileCount));
    }
}

void ModListWidget::onModUninstalled(quint64 requestId, const QString& gameId, const QString& modName,
                                     const QStringList& flaggedArchives)
{
    if (!matchesModAction(requestId, gameId, modName, ModActionKind::Uninstall))
        return;
    Q_UNUSED(flaggedArchives);
    finishModAction(true);
}

void ModListWidget::onModRenamed(quint64 requestId, const QString& gameId, const QString& oldName,
                                 const QString& newName)
{
    if (!matchesModAction(requestId, gameId, oldName, ModActionKind::Rename)
        || m_modAction->newName != newName)
        return;
    finishModAction(true);
}

void ModListWidget::onModActionFailed(quint64 requestId, const QString& gameId, const QString& modName,
                                      const QString& method, const QString& error)
{
    if (!m_modAction || requestId != m_modAction->requestId || gameId != m_modAction->context.gameId
        || modName != m_modAction->folder)
        return;
    const ModAction action = *m_modAction;
    const QString expected = action.kind == ModActionKind::Reinstall ? QStringLiteral("ReinstallMod")
        : action.kind == ModActionKind::Uninstall ? QStringLiteral("UninstallMod") : QStringLiteral("RenameMod");
    if (method != expected)
        return;
    const bool current = matchesContext(action.context);
    const InstallError inUse = parseInstallError(error);
    if (action.kind == ModActionKind::Uninstall && !action.forced && current
        && inUse.token == QLatin1String("mod_in_use")) {
        m_modAction->requestId = 0;
        const QString profiles = inUse.fields.value(QStringLiteral("profiles"));
        if (dialogs::confirm(this, QStringLiteral("Mod In Use"),
            QStringLiteral("\"%1\" is enabled in profile(s): %2\n\n"
                           "Uninstall anyway? The mod will also be removed from "
                           "those profiles' mod lists.").arg(action.name, profiles))) {
            if (matchesContext(action.context) && availableModIndex(action.context, action.folder) >= 0) {
                m_modAction->forced = true;
                m_modAction->submittedWhileConnected = m_grpc->isConnected();
                m_modAction->requestId = m_grpc->uninstallModAsync(gameId, modName, true);
                return;
            }
        }
        finishModAction(false);
        return;
    }
    const QString displayError = modActionError(error, action.submittedWhileConnected);
    if (m_bulkReinstall) {
        ++m_bulkReinstall->failed;
        m_bulkReinstall->errors.append(QStringLiteral("• %1: %2\n%3")
            .arg(action.name, errorSummary(QStringLiteral("reinstall this mod"), displayError, true), error));
        finishModAction(false);
        return;
    }
    finishModAction(false);
    if (!current)
        return;
    const QString title = action.kind == ModActionKind::Reinstall ? QStringLiteral("Reinstall Failed")
        : action.kind == ModActionKind::Uninstall ? QStringLiteral("Uninstall Failed")
        : QStringLiteral("Rename Failed");
    const QString operation = action.kind == ModActionKind::Reinstall ? QStringLiteral("reinstall this mod")
        : action.kind == ModActionKind::Uninstall ? QStringLiteral("uninstall this mod")
        : QStringLiteral("rename this mod");
    presentError(this, title, operation, displayError, true, error);
}

void ModListWidget::onReinstallFailed(quint64 requestId, const QString& error)
{
    if (!m_modAction || m_modAction->kind != ModActionKind::Reinstall ||
        m_modAction->requestId != requestId) return;
    onModActionFailed(requestId, m_modAction->context.gameId, m_modAction->folder,
                      QStringLiteral("ReinstallMod"), error);
}

void ModListWidget::onReinstallCancelled(quint64 requestId)
{
    if (!m_modAction || m_modAction->kind != ModActionKind::Reinstall ||
        m_modAction->requestId != requestId) return;
    const bool bulk = m_bulkReinstall.has_value();
    if (bulk) {
        m_bulkReinstall->stopRequested = true;
        m_bulkReinstall->folders.resize(m_bulkReinstall->next);
    }
    finishModAction(false);
    if (!bulk)
        dialogs::info(this, "Reinstall Cancelled", "Reinstall cancelled. The original mod was not changed.");
}

void ModListWidget::onReinstallUnknown(quint64 requestId)
{
    if (!m_modAction || m_modAction->kind != ModActionKind::Reinstall ||
        m_modAction->requestId != requestId) return;
    const bool bulk = m_bulkReinstall.has_value();
    const bool current = matchesContext(m_modAction->context);
    if (bulk) {
        ++m_bulkReinstall->unknown;
        m_bulkReinstall->notices.append(QStringLiteral("• %1: Reinstall result could not be confirmed. "
                                                     "Check Mods and Downloads before trying again.")
                                             .arg(m_modAction->name));
        m_bulkReinstall->stopRequested = true;
        m_bulkReinstall->folders.resize(m_bulkReinstall->next);
    }
    finishModAction(false);
    if (current) {
        reloadMods();
        emit modsEdited();
    }
    if (!bulk)
        dialogs::plainWarn(this, "Reinstall Result Unknown",
            "Gorganizer could not confirm whether this mod was reinstalled. Check Mods and Downloads before trying again.");
}

void ModListWidget::onModActionWorkersStopped()
{
    if (!m_modAction || m_modAction->requestId == 0 ||
        m_modAction->kind == ModActionKind::Reinstall)
        return;
    const ModAction action = *m_modAction;
    const QString method = action.kind == ModActionKind::Uninstall ? QStringLiteral("UninstallMod")
        : QStringLiteral("RenameMod");
    onModActionFailed(action.requestId, action.context.gameId, action.folder, method,
                      action.submittedWhileConnected ? QStringLiteral("deadline exceeded")
                          : QStringLiteral("not connected"));
}

void ModListWidget::showContextMenu(const QPoint& pos)
{
    auto idx = m_view->indexAt(pos);
    int row = idx.isValid() ? idx.row() : -1;

    QMenu menu;
    const ActionContext context = actionContext();
    const bool locked = editsBlocked();

    if (row >= 0) {
        const ModListRow clickedRow = m_model->rowAt(row);
        if (clickedRow.kind == RowKindSeparator) {
            const QString name = clickedRow.name;
            menu.addAction("Toggle Collapse", [this, context, name] { toggleCollapseAt(context, name); });
            menu.addAction("Rename Separator...", [this, context, name] { renameSeparator(context, name); });
            menu.addSeparator();
            menu.addAction("Move to Top", [this, context, name] { moveSeparatorTo(context, name, true); })->setEnabled(!locked);
            menu.addAction("Move to Bottom", [this, context, name] { moveSeparatorTo(context, name, false); })->setEnabled(!locked);
            menu.addSeparator();
            menu.addAction("Remove Separator", [this, context, name] { removeSeparator(context, name); });
            menu.exec(m_view->viewport()->mapToGlobal(pos));
            return;
        }
        if (clickedRow.kind == RowKindOverwrite) {
            onOverwriteContextMenu(context, m_view->viewport()->mapToGlobal(pos));
            return;
        }
    }

    if (m_visualMode) {
        ModRowKind anchorKind = RowKindOverwrite;
        QString anchorName;
        if (row >= 0) {
            const ModListRow anchor = m_model->rowAt(row);
            anchorKind = anchor.kind;
            anchorName = anchor.kind == RowKindMod ? anchor.folder : anchor.name;
        }
        menu.addAction("Add Separator Here...", [this, context, anchorKind, anchorName] {
            createSeparatorAt(context, anchorKind, anchorName);
        })->setEnabled(!locked);
        menu.addAction("Group by Category", [this, context] {
            if (matchesContext(context))
                groupByCategory();
        })->setEnabled(!locked);
        menu.addSeparator();
    }

    if (row < 0) {
        if (!menu.isEmpty())
            menu.exec(m_view->viewport()->mapToGlobal(pos));
        return;
    }
    const ModListRow clicked = m_model->rowAt(row);
    if (clicked.kind != RowKindMod)
        return;
    const int modIdx = modIndexForFolder(clicked.folder);
    if (modIdx < 0)
        return;
    const ModMetadata meta = m_mods[modIdx];

    QStringList selectedFolders;
    QStringList selectedNames;
    {
        QSet<int> rows;
        for (const QModelIndex& sel : m_view->selectionModel()->selectedRows())
            rows.insert(sel.row());
        rows.insert(row);
        QList<int> sortedRows = rows.values();
        std::sort(sortedRows.begin(), sortedRows.end());
        for (int r : sortedRows) {
            const ModListRow& mr = m_model->rowAt(r);
            if (mr.kind != RowKindMod)
                continue;
            const int idx = modIndexForFolder(mr.folder);
            if (idx < 0 || selectedFolders.contains(mr.folder))
                continue;
            selectedFolders.append(mr.folder);
            selectedNames.append(m_mods[idx].name);
        }
    }
    if (selectedFolders.size() >= 2) {
        QString summary = QString("%1 mods selected").arg(selectedFolders.size());
        auto* header = menu.addAction(summary);
        header->setEnabled(false);
        menu.addSeparator();

        menu.addAction("Enable All", [this, context, selectedFolders] {
            setSelectedModsEnabled(context, selectedFolders, true);
        })->setEnabled(!locked);
        menu.addAction("Disable All", [this, context, selectedFolders] {
            setSelectedModsEnabled(context, selectedFolders, false);
        })->setEnabled(!locked);
        menu.addSeparator();

        bool allReinstallable = true;
        for (const QString& folder : selectedFolders) {
            const int index = modIndexForFolder(folder);
            if (index < 0 || m_mods[index].sourceArchives.isEmpty()) {
                allReinstallable = false;
                break;
            }
        }
        auto* bulkReinstall = menu.addAction(QString("Reinstall %1 Mods").arg(selectedFolders.size()));
        bulkReinstall->setEnabled(allReinstallable);
        if (!allReinstallable)
            bulkReinstall->setToolTip("One or more selected mods have no source archives.");
        connect(bulkReinstall, &QAction::triggered, this,
                [this, context, selectedFolders, selectedNames]() {
            if (refuseModAction())
                return;
            if (!dialogs::confirm(this, "Reinstall Mods",
                QString("Reinstall %1 mods by replaying their source archives?\n\n"
                        "Each mod is rebuilt from its archives and replaced only if every archive installs.")
                    .arg(selectedFolders.size())))
                return;
            if (!matchesContext(context) || refuseModAction())
                return;
            startBulkReinstall(context, selectedFolders, selectedNames);
        });
        menu.exec(m_view->viewport()->mapToGlobal(pos));
        return;
    }

    const QString folder = meta.folder;
    if (!meta.nexusUrl.isEmpty()) {
        menu.addAction("Visit Mod Page", [this, context, folder, url = meta.nexusUrl] {
            if (availableModIndex(context, folder) >= 0)
                openWebLink(this, url);
        });
    }
    menu.addAction(meta.nexusUrl.isEmpty() ? "Set Mod Page URL..." : "Change Mod Page URL...",
        [this, context, folder, currentUrl = meta.nexusUrl] {
            bool ok = false;
            QString url = QInputDialog::getText(m_view, "Mod Page URL",
                "Paste a URL (e.g. Nexus Mods page). Leave empty to clear.",
                QLineEdit::Normal, currentUrl, &ok);
            if (ok)
                updateModPageUrl(context, folder, url.trimmed());
        });
    if (showsModDependencies(m_activeGame))
        addDependencyActions(menu, meta.folder);
    menu.addSeparator();

    menu.addAction("Show Conflicts...", [this, context, folder] {
        const int index = availableModIndex(context, folder);
        if (index >= 0)
            showConflictDetailsForMod(m_mods[index].name);
    });

    menu.addAction("Open Mod Folder", [this, context, folder] {
        if (availableModIndex(context, folder) >= 0)
            QDesktopServices::openUrl(QUrl::fromLocalFile(context.modsDir + "/" + folder));
    });

    auto* catMenu = menu.addMenu("Set Category");
    for (const auto& cat : defaultCategories()) {
        catMenu->addAction(cat, [this, context, folder, cat] { setCategoryForFolder(context, folder, cat); });
    }
    catMenu->addSeparator();
    catMenu->addAction("Custom...", [this, context, folder] {
        bool ok = false;
        QString custom = QInputDialog::getText(m_view, "Custom Category",
            "Enter category name:", QLineEdit::Normal, "", &ok);
        if (ok && !custom.trimmed().isEmpty())
            setCategoryForFolder(context, folder, custom.trimmed());
    });

    menu.addSeparator();

    {
        auto* reinstall = menu.addAction("Reinstall");
        bool haveArchives = !meta.sourceArchives.isEmpty();
        reinstall->setEnabled(haveArchives);
        if (haveArchives) {
            reinstall->setToolTip(
                QString("Replays %1 archive(s) in install order.").arg(meta.sourceArchives.size()));
            connect(reinstall, &QAction::triggered, this, [this, context, meta] {
                if (refuseModAction())
                    return;
                if (!dialogs::confirm(this, "Reinstall Mod",
                    QString("Reinstall \"%1\" by replaying %2 archive(s)?\n\n"
                            "The mod is rebuilt from its archives in the order they were installed "
                            "and replaced only if every archive installs.")
                        .arg(meta.name).arg(meta.sourceArchives.size())))
                    return;
                if (refuseModAction() || availableModIndex(context, meta.folder) < 0)
                    return;
                startModAction(ModAction{ModActionKind::Reinstall, context, meta.folder, meta.name});
            });
        } else {
            reinstall->setToolTip("No source archives recorded for this mod.");
        }
    }

    menu.addSeparator();

    menu.addAction("Rename Mod...", [this, context, meta] {
        if (refuseModAction())
            return;
        bool ok = false;
        QString newName = QInputDialog::getText(this, "Rename Mod",
            "New name (also becomes the folder name on disk):",
            QLineEdit::Normal, meta.folder, &ok);
        if (!ok || newName.isEmpty() || newName == meta.folder) return;
        if (refuseModAction() || availableModIndex(context, meta.folder) < 0)
            return;
        startModAction(ModAction{ModActionKind::Rename, context, meta.folder, meta.name, newName});
    });

    menu.addAction("Uninstall Mod", [this, context, meta] {
        if (refuseModAction())
            return;
        if (!dialogs::confirm(this, "Uninstall Mod",
            QString("Uninstall \"%1\"?\n\n"
                    "The mod folder will be removed and its archive will be "
                    "marked Uninstalled in the Downloads tab (the archive "
                    "itself is kept so you can reinstall later).")
                .arg(meta.name))) return;
        if (refuseModAction() || availableModIndex(context, meta.folder) < 0)
            return;
        startModAction(ModAction{ModActionKind::Uninstall, context, meta.folder, meta.name});
    });

    menu.exec(m_view->viewport()->mapToGlobal(pos));
}

// Cycles asc → desc → back to priority-ascending; drag is only enabled in priority-ascending.
void ModListWidget::onHeaderClicked(int column)
{
    if (m_sortColumn == column) {
        if (m_sortOrder == Qt::AscendingOrder) {
            m_sortOrder = Qt::DescendingOrder;
        } else {
            m_sortColumn = ModColPriority;
            m_sortOrder = Qt::AscendingOrder;
            m_view->header()->setSortIndicator(ModColPriority, Qt::AscendingOrder);
            restorePriorityOrder();
            m_view->setDragEnabled(true);
            return;
        }
    } else {
        m_sortColumn = column;
        m_sortOrder = Qt::AscendingOrder;
    }

    m_view->header()->setSortIndicator(m_sortColumn, m_sortOrder);

    if (m_sortColumn == ModColPriority && m_sortOrder == Qt::AscendingOrder) {
        restorePriorityOrder();
        m_view->setDragEnabled(true);
    } else {
        m_view->setDragEnabled(false);
        m_model->sortBy(m_sortColumn, m_sortOrder);
    }
}

void ModListWidget::restorePriorityOrder()
{
    if (m_visualMode) {
        rebuildView();
        return;
    }
    m_model->restorePriorityOrder();
}

void ModListWidget::updateModPageUrl(const ActionContext& context, const QString& folder, const QString& url)
{
    if (m_modActionInProgress || availableModIndex(context, folder) < 0)
        return;
    QFile file(metadataPathForFolder(folder));
    if (!file.open(QIODevice::ReadOnly | QIODevice::Text))
        return;
    QString key = "mod_page";
    for (const QString& line : QString::fromUtf8(file.readAll()).split('\n')) {
        if (line.startsWith("mod_page:"))
            key = "mod_page";
        else if (line.startsWith("nexus_url:"))
            key = "nexus_url";
    }
    file.close();
    const int index = availableModIndex(context, folder);
    if (index < 0)
        return;
    ModCatalog::patchMetadataField(metadataPathForFolder(folder), key, url);
    m_mods[index].nexusUrl = url;
}

bool ModListWidget::visualModeEnabled() const
{
    return m_visualMode;
}

namespace {
struct VisualKey {
    quint64 sepIdx = 0;
    int kind = 0;
    quint64 own = 0;
    int stableTie = 0;
    bool operator<(const VisualKey& o) const {
        if (sepIdx != o.sepIdx) return sepIdx < o.sepIdx;
        if (kind != o.kind) return kind < o.kind;
        if (own != o.own) return own < o.own;
        return stableTie < o.stableTie;
    }
};
}

void ModListWidget::onVisualToggled(bool on)
{
    if (m_modActionInProgress)
        return;
    m_visualMode = on;
    if (isInteracting())
        m_reloadPending = true;
    else
        rebuildView();
    persistSeparators();
}

void ModListWidget::applyCollapsedSeparatorView(bool on)
{
    if (m_collapsedSeparatorView == on && (!on || (m_visualMode && !m_visualCheck->isEnabled())))
        return;

    m_collapsedSeparatorView = on;
    if (!m_visualCheck) return;

    if (on) {
        QSignalBlocker block(m_visualCheck);
        m_visualCheck->setChecked(true);
        m_visualCheck->setEnabled(false);
        bool wasVisual = m_visualMode;
        m_visualMode = true;
        if (isInteracting())
            m_reloadPending = true;
        else
            rebuildView();
        if (!wasVisual)
            persistSeparators();
    } else {
        m_visualCheck->setEnabled(!m_modActionInProgress);
    }
}

// Rebuilds the model rows: true load order flat, or separator-grouped visual order.
void ModListWidget::rebuildView()
{
    m_updatingModel = true;

    struct Ordered {
        ModListRow row;
        VisualKey vk;
    };
    std::vector<Ordered> ordered;

    auto makeModRow = [&](int modIdx) -> ModListRow {
        const auto& meta = m_mods[modIdx];
        ModListRow r;
        r.kind = RowKindMod;
        r.modIndex = modIdx;
        r.folder = meta.folder;
        r.name = meta.name;
        r.category = meta.category;
        r.version = meta.version;
        r.checked = meta.enabled;
        return r;
    };

    auto makeSeparatorRow = [&](int sepIdx) -> ModListRow {
        const auto& sep = m_separators[sepIdx];
        ModListRow r;
        r.kind = RowKindSeparator;
        r.name = sep.name;
        r.collapsed = sep.collapsed;
        return r;
    };

    if (!m_visualMode) {
        std::vector<int> idx(m_mods.size());
        for (size_t i = 0; i < m_mods.size(); ++i) idx[i] = int(i);
        std::stable_sort(idx.begin(), idx.end(), [&](int a, int b) {
            quint64 ka = parseHexIndex(m_mods[a].trueIndex);
            quint64 kb = parseHexIndex(m_mods[b].trueIndex);
            if (ka == 0 && kb == 0) return a < b;
            if (ka == 0) return false;
            if (kb == 0) return true;
            return ka < kb;
        });
        for (int i : idx)
            ordered.push_back({makeModRow(i), {}});
    } else {
        QHash<QString, quint64> sepRank;
        QHash<QString, bool> sepCollapsed;
        for (const auto& s : m_separators) {
            sepRank[s.name] = parseHexIndex(s.visualIndex);
            sepCollapsed[s.name] = s.collapsed;
        }

        for (int i = 0; i < int(m_separators.size()); ++i) {
            Ordered o{makeSeparatorRow(i), {}};
            o.vk.sepIdx = parseHexIndex(m_separators[i].visualIndex);
            o.vk.kind = 0;
            ordered.push_back(std::move(o));
        }
        for (int i = 0; i < int(m_mods.size()); ++i) {
            const auto& m = m_mods[i];
            quint64 sepIdx;
            if (!m.separator.isEmpty() && sepRank.contains(m.separator))
                sepIdx = sepRank[m.separator];
            else
                sepIdx = std::numeric_limits<quint64>::max();
            quint64 own = parseHexIndex(m.visualIndex);
            if (own == 0) own = parseHexIndex(m.trueIndex);

            if (!m.separator.isEmpty() && sepCollapsed.value(m.separator, false))
                continue;

            Ordered o{makeModRow(i), {}};
            o.vk.sepIdx = sepIdx;
            o.vk.kind = 1;
            o.vk.own = own;
            o.vk.stableTie = i;
            ordered.push_back(std::move(o));
        }
        std::stable_sort(ordered.begin(), ordered.end(),
            [](const Ordered& a, const Ordered& b) { return a.vk < b.vk; });
    }

    std::vector<ModListRow> rows;
    rows.reserve(ordered.size());
    for (auto& o : ordered)
        rows.push_back(std::move(o.row));
    m_model->setRows(std::move(rows));
    applyOverwriteSpan();

    m_updatingModel = false;
}

// Re-applies the full-width span of the pinned Overwrite row after a model repopulation.
void ModListWidget::applyOverwriteSpan()
{
    int last = m_model->rowCount() - 1;
    for (int r = 0; r <= last; ++r)
        m_view->setFirstColumnSpanned(r, QModelIndex(),
                                      r == last && m_model->rowAt(r).kind == RowKindOverwrite);
}

// Groups the mods hidden under collapsed separators by separator, each group in its existing visual order.
QHash<QString, std::vector<int>> ModListWidget::hiddenModsBySeparator() const
{
    std::vector<bool> shown(m_mods.size(), false);
    for (int r = 0; r < m_model->rowCount(); ++r) {
        const ModListRow& row = m_model->rowAt(r);
        if (row.kind == RowKindMod && row.modIndex >= 0 && row.modIndex < int(m_mods.size()))
            shown[row.modIndex] = true;
    }
    QSet<QString> collapsed;
    for (const auto& s : m_separators) {
        if (s.collapsed)
            collapsed.insert(s.name);
    }
    QHash<QString, std::vector<int>> hidden;
    for (int i = 0; i < int(m_mods.size()); ++i) {
        const QString& sep = m_mods[i].separator;
        if (!shown[i] && !sep.isEmpty() && collapsed.contains(sep))
            hidden[sep].push_back(i);
    }
    auto ownIndex = [this](int modIdx) {
        quint64 own = parseHexIndex(m_mods[modIdx].visualIndex);
        return own != 0 ? own : parseHexIndex(m_mods[modIdx].trueIndex);
    };
    for (auto it = hidden.begin(); it != hidden.end(); ++it) {
        std::stable_sort(it.value().begin(), it.value().end(), [&](int a, int b) {
            return ownIndex(a) < ownIndex(b);
        });
    }
    return hidden;
}

// Saves the priority row order through the queue or stamps visual indices into mod metadata.
void ModListWidget::persistRowOrder()
{
    if (m_updatingModel || editsBlocked()) return;

    if (!m_visualMode) {
        if (m_gameId.isEmpty() || m_profileName.isEmpty())
            return;
        std::vector<GrpcModListEntry> entries;
        for (int r = 0; r < m_model->rowCount(); ++r) {
            const ModListRow& row = m_model->rowAt(r);
            if (row.kind != RowKindMod) continue;
            GrpcModListEntry e;
            e.modName = row.folder;
            if (e.modName.isEmpty()) e.modName = row.name;
            e.enabled = row.checked;
            e.priority = r;
            entries.push_back(std::move(e));
        }
        submitModList(entries);
        return;
    }

    QString currentSeparator;
    quint64 runningIdx = 0x10;
    std::vector<SeparatorDef> updatedSeparators;
    std::vector<GrpcModListEntry> collapsedEntries;
    QHash<QString, std::vector<int>> hiddenChildren = hiddenModsBySeparator();

    auto stampMod = [&](int modIdx, const QString& newSeparator) {
        auto& meta = m_mods[modIdx];
        QString newVisualIndex = formatHexIndex(runningIdx);
        bool dirty = (meta.visualIndex != newVisualIndex) || (meta.separator != newSeparator);
        if (m_collapsedSeparatorView && meta.trueIndex != newVisualIndex)
            dirty = true;
        meta.visualIndex = newVisualIndex;
        meta.separator = newSeparator;
        if (m_collapsedSeparatorView)
            meta.trueIndex = newVisualIndex;
        if (dirty) {
            const QString yamlPath = metadataPathForFolder(meta.folder);
            if (!yamlPath.isEmpty()) {
                ModCatalog::patchMetadataField(yamlPath, "visual_index", newVisualIndex);
                ModCatalog::patchMetadataField(yamlPath, "separator", newSeparator);
                if (m_collapsedSeparatorView)
                    ModCatalog::patchMetadataField(yamlPath, "true_index", newVisualIndex);
            }
        }
        if (m_collapsedSeparatorView) {
            GrpcModListEntry e;
            e.modName = meta.folder;
            e.enabled = meta.enabled;
            e.priority = int(collapsedEntries.size());
            collapsedEntries.push_back(std::move(e));
        }
        runningIdx += 0x10;
    };
    auto flushHiddenChildren = [&](const QString& sepName) {
        auto it = hiddenChildren.find(sepName);
        if (sepName.isEmpty() || it == hiddenChildren.end())
            return;
        const std::vector<int> children = it.value();
        hiddenChildren.erase(it);
        for (int modIdx : children)
            stampMod(modIdx, sepName);
    };

    for (int r = 0; r < m_model->rowCount(); ++r) {
        const ModListRow& row = m_model->rowAt(r);
        if (row.kind == RowKindSeparator) {
            flushHiddenChildren(currentSeparator);
            QString sepName = row.name;
            currentSeparator = sepName;
            SeparatorDef d;
            d.name = sepName;
            d.visualIndex = formatHexIndex(runningIdx);
            for (const auto& s : m_separators) {
                if (s.name == sepName) { d.collapsed = s.collapsed; break; }
            }
            updatedSeparators.push_back(d);
            runningIdx += 0x10;
            continue;
        }
        if (row.kind != RowKindMod) continue;
        int modIdx = row.modIndex;
        if (modIdx < 0 || modIdx >= int(m_mods.size())) continue;
        stampMod(modIdx, currentSeparator);
    }
    flushHiddenChildren(currentSeparator);

    m_separators = updatedSeparators;
    persistSeparators();

    if (m_collapsedSeparatorView && !m_gameId.isEmpty() && !m_profileName.isEmpty()) {
        submitModList(collapsedEntries);
    }
}

void ModListWidget::persistSeparators()
{
    if (m_gameId.isEmpty() || m_profileName.isEmpty())
        return;
    std::vector<GrpcSeparator> out;
    out.reserve(m_separators.size());
    for (const auto& s : m_separators) {
        GrpcSeparator g;
        g.name = s.name;
        g.visualIndex = s.visualIndex;
        g.collapsed = s.collapsed;
        out.push_back(std::move(g));
    }
    QString err;
    m_grpc->setSeparators(m_gameId, m_profileName, out, m_visualMode, err);
}

void ModListWidget::createSeparatorAt(const ActionContext& context, ModRowKind anchorKind,
                                      const QString& anchorName)
{
    if (editsBlocked() || !matchesContext(context))
        return;
    bool ok = false;
    QString name = QInputDialog::getText(m_view, "New Separator",
        "Separator name:", QLineEdit::Normal, "", &ok);
    name = name.trimmed();
    if (!ok || name.isEmpty())
        return;
    int targetRow = m_model->overwriteRow();
    if (anchorKind == RowKindMod) {
        const int index = availableModIndex(context, anchorName);
        if (index < 0)
            return;
        targetRow = m_model->rowForModIndex(index);
        if (targetRow < 0)
            return;
    } else if (anchorKind == RowKindSeparator) {
        if (availableSeparatorIndex(context, anchorName) < 0)
            return;
        targetRow = m_model->rowForSeparatorName(anchorName);
    } else if (!matchesContext(context)) {
        return;
    }
    if (editsBlocked())
        return;
    for (const auto& s : m_separators) {
        if (s.name.compare(name, Qt::CaseInsensitive) == 0) {
            dialogs::warn(this, "Duplicate",
                "A separator with that name already exists in this profile.");
            return;
        }
    }
    SeparatorDef d;
    d.name = name;
    d.collapsed = false;
    d.visualIndex = formatHexIndex(0);
    m_separators.push_back(d);
    rebuildView();
    int sepRow = m_model->rowForSeparatorName(name);
    if (sepRow >= 0 && targetRow >= 0 && targetRow != sepRow)
        m_model->moveRowsTo({sepRow}, targetRow);
    persistRowOrder();
}

void ModListWidget::renameSeparator(const ActionContext& context, const QString& oldName)
{
    if (editsBlocked() || availableSeparatorIndex(context, oldName) < 0)
        return;
    bool ok = false;
    QString newName = QInputDialog::getText(m_view, "Rename Separator",
        "New name:", QLineEdit::Normal, oldName, &ok);
    newName = newName.trimmed();
    if (!ok || newName.isEmpty() || newName == oldName || editsBlocked()) return;
    const int index = availableSeparatorIndex(context, oldName);
    if (index < 0)
        return;
    for (const auto& s : m_separators) {
        if (s.name.compare(newName, Qt::CaseInsensitive) == 0) {
            dialogs::warn(this, "Duplicate", "A separator with that name already exists in this profile.");
            return;
        }
    }
    for (auto& meta : m_mods) {
        if (meta.separator == oldName) {
            const QString yamlPath = metadataPathForFolder(meta.folder);
            if (!yamlPath.isEmpty()) {
                meta.separator = newName;
                ModCatalog::patchMetadataField(yamlPath, "separator", newName);
            }
        }
    }
    m_separators[index].name = newName;
    persistSeparators();
    rebuildView();
}

void ModListWidget::removeSeparator(const ActionContext& context, const QString& name)
{
    if (editsBlocked() || availableSeparatorIndex(context, name) < 0)
        return;
    for (auto& meta : m_mods) {
        if (meta.separator == name) {
            const QString yamlPath = metadataPathForFolder(meta.folder);
            if (!yamlPath.isEmpty()) {
                meta.separator.clear();
                ModCatalog::patchMetadataField(yamlPath, "separator", QString());
            }
        }
    }
    m_separators.erase(std::remove_if(m_separators.begin(), m_separators.end(),
        [&](const SeparatorDef& s) { return s.name == name; }), m_separators.end());
    persistSeparators();
    rebuildView();
}

void ModListWidget::toggleCollapseAt(const ActionContext& context, const QString& name)
{
    if (editsBlocked())
        return;
    const int index = availableSeparatorIndex(context, name);
    if (index < 0)
        return;
    m_separators[index].collapsed = !m_separators[index].collapsed;
    persistSeparators();
    rebuildView();
}

// Brackets the separator's index outside the current min/max so rebuildView sorts it to the desired end.
void ModListWidget::moveSeparatorTo(const ActionContext& context, const QString& name, bool toTop)
{
    if (editsBlocked() || availableSeparatorIndex(context, name) < 0)
        return;
    if (name.isEmpty() || m_separators.empty())
        return;

    quint64 lowest = parseHexIndex(m_separators.front().visualIndex);
    quint64 highest = lowest;
    for (const auto& s : m_separators) {
        quint64 v = parseHexIndex(s.visualIndex);
        if (v < lowest) lowest = v;
        if (v > highest) highest = v;
    }
    quint64 newIdx = toTop
        ? (lowest > 0x10 ? lowest - 0x10 : 0x1)
        : (highest + 0x10);

    for (auto& s : m_separators) {
        if (s.name == name) {
            s.visualIndex = formatHexIndex(newIdx);
            break;
        }
    }

    rebuildView();
    persistRowOrder();
}

void ModListWidget::setCategoryForFolder(const ActionContext& context, const QString& folder,
                                         const QString& category)
{
    if (m_modActionInProgress)
        return;
    const int index = availableModIndex(context, folder);
    if (index < 0)
        return;
    const QString metaPath = metadataPathForFolder(folder);
    ModCatalog::patchMetadataField(metaPath, "category", category);
    m_mods[index].category = category;
    const int row = m_model->rowForModIndex(index);
    if (row >= 0)
        m_model->setCategoryAt(row, category);
}

// Builds the right-click menu for the pinned Overwrite row.
void ModListWidget::onOverwriteContextMenu(const ActionContext& context, const QPoint& globalPos)
{
    if (refuseModAction())
        return;
    QMenu menu;

    std::vector<GrpcOverwriteEntry> files;
    QString owDir, err;
    bool ok = m_grpc->listOverwriteFiles(context.gameId, files, owDir, err);
    if (!matchesContext(context))
        return;
    bool hasFiles = false;
    for (const auto& f : files) {
        if (!f.isDir) { hasFiles = true; break; }
    }

    auto* openAct = menu.addAction("Open Overwrite Folder");
    connect(openAct, &QAction::triggered, this, [this, context, owDir] {
        if (!matchesContext(context))
            return;
        QString dir = owDir;
        if (dir.isEmpty()) {
            dir = context.modsDir + "/" + kOverwriteModName;
            QDir().mkpath(dir);
        }
        QDesktopServices::openUrl(QUrl::fromLocalFile(dir));
    });
    menu.addSeparator();

    auto* extractAll = menu.addAction("Extract All to New Mod...");
    extractAll->setEnabled(ok && hasFiles);
    if (ok && !hasFiles)
        extractAll->setToolTip("Overwrite is empty.");
    if (!ok)
        extractAll->setToolTip(plainToolTip(errorSummary("list Overwrite files", err)));
    connect(extractAll, &QAction::triggered, this, [this, context] { extractOverwriteAll(context); });

    auto* extractSel = menu.addAction("Extract Selected Files to New Mod...");
    extractSel->setEnabled(ok && hasFiles);
    connect(extractSel, &QAction::triggered, this, [this, context] { extractOverwriteSelected(context); });

    menu.exec(globalPos);
}

void ModListWidget::extractOverwriteAll(const ActionContext& context)
{
    if (refuseModAction() || !matchesContext(context))
        return;
    bool ok = false;
    QString name = QInputDialog::getText(this, "Extract Overwrite",
        "New mod name (empty list will extract every file in Overwrite):",
        QLineEdit::Normal, "Overwrite Snapshot", &ok);
    if (!ok || name.trimmed().isEmpty() || !matchesContext(context) || refuseModAction())
        return;
    int count = 0;
    QString err;
    const bool extracted = m_grpc->extractOverwriteToMod(context.gameId, name.trimmed(), {}, false, count, err);
    if (!matchesContext(context))
        return;
    if (!extracted) {
        presentError(this, "Extract Failed", "extract Overwrite files into a mod", err, true);
        return;
    }
    dialogs::info(this, "Extract Complete",
        QString("Moved %1 file(s) into mod \"%2\".").arg(count).arg(name.trimmed()));
    reloadMods();
}

// Pops a multi-select picker so the user can graduate a subset of Overwrite into a named mod folder.
void ModListWidget::extractOverwriteSelected(const ActionContext& context)
{
    if (refuseModAction() || !matchesContext(context))
        return;
    std::vector<GrpcOverwriteEntry> files;
    QString owDir, err;
    const bool listed = m_grpc->listOverwriteFiles(context.gameId, files, owDir, err);
    if (!matchesContext(context))
        return;
    if (!listed) {
        presentError(this, "Extract Failed", "list Overwrite files", err);
        return;
    }

    QDialog dlg(this);
    dlg.setWindowTitle("Extract Files from Overwrite");
    auto* outer = new QVBoxLayout(&dlg);

    auto* hint = new QLabel(
        "Select files to graduate into a new mod folder.\n"
        "Hold Ctrl or Shift to multi-select; click and drag to rubber-band.\n"
        "Directory entries are skipped — only files are extracted.");
    hint->setWordWrap(true);
    outer->addWidget(hint);

    auto* list = new QListWidget;
    list->setSelectionMode(QAbstractItemView::ExtendedSelection);
    list->setSelectionRectVisible(true);
    list->setUniformItemSizes(true);
    list->setAlternatingRowColors(true);
    int fileRows = 0;
    for (const auto& e : files) {
        if (e.isDir)
            continue;
        QString sizeStr;
        if (e.sizeBytes < 1024)
            sizeStr = QString("%1 B").arg(e.sizeBytes);
        else if (e.sizeBytes < 1024 * 1024)
            sizeStr = QString("%1 KB").arg(e.sizeBytes / 1024);
        else
            sizeStr = QString("%1 MB").arg(e.sizeBytes / (1024 * 1024));
        auto* item = new QListWidgetItem(QString("%1   (%2)").arg(e.relPath, sizeStr));
        item->setData(Qt::UserRole, e.relPath);
        list->addItem(item);
        ++fileRows;
    }
    outer->addWidget(list, 1);

    if (fileRows == 0) {
        auto* msg = new QLabel("<i>Overwrite contains no files.</i>");
        msg->setTextFormat(Qt::RichText);
        outer->addWidget(msg);
    }

    auto* nameRow = new QHBoxLayout;
    nameRow->addWidget(new QLabel("New mod name:"));
    auto* nameEdit = new QLineEdit("Overwrite Selection");
    nameRow->addWidget(nameEdit, 1);
    outer->addLayout(nameRow);

    auto* keepCb = new QCheckBox("Keep originals in Overwrite (copy instead of move)");
    outer->addWidget(keepCb);

    auto* btns = new QDialogButtonBox(QDialogButtonBox::Ok | QDialogButtonBox::Cancel);
    btns->button(QDialogButtonBox::Ok)->setText("Extract");
    outer->addWidget(btns);
    connect(btns, &QDialogButtonBox::accepted, &dlg, &QDialog::accept);
    connect(btns, &QDialogButtonBox::rejected, &dlg, &QDialog::reject);
    btns->button(QDialogButtonBox::Ok)->setDefault(true);
    fitToScreen(&dlg, QSize(720, 520));

    if (dlg.exec() != QDialog::Accepted || !matchesContext(context) || refuseModAction())
        return;

    QStringList chosen;
    for (auto* it : list->selectedItems())
        chosen.append(it->data(Qt::UserRole).toString());
    if (chosen.isEmpty()) {
        dialogs::info(this, "Extract", "Nothing selected.");
        return;
    }
    QString name = nameEdit->text().trimmed();
    if (name.isEmpty()) {
        dialogs::warn(this, "Extract", "Mod name is required.");
        return;
    }

    int count = 0;
    QString rpcErr;
    const bool extracted = m_grpc->extractOverwriteToMod(context.gameId, name, chosen, keepCb->isChecked(),
                                                          count, rpcErr);
    if (!matchesContext(context))
        return;
    if (!extracted) {
        presentError(this, "Extract Failed", "extract selected Overwrite files into a mod", rpcErr, true);
        return;
    }
    reloadMods();
    dialogs::info(this, "Extract Complete",
        QString("Moved %1 file(s) into mod \"%2\".").arg(count).arg(name));
}

void ModListWidget::onAddSeparatorClicked()
{
    if (m_view->isHidden() || editsBlocked())
        return;
    beginInteraction();
    if (!m_visualMode)
        m_visualCheck->setChecked(true);
    bool atTop = QApplication::keyboardModifiers().testFlag(Qt::ShiftModifier);
    ModRowKind anchorKind = RowKindOverwrite;
    QString anchorName;
    if (atTop && m_model->rowCount() > 0) {
        const ModListRow anchor = m_model->rowAt(0);
        anchorKind = anchor.kind;
        anchorName = anchor.kind == RowKindMod ? anchor.folder : anchor.name;
    }
    createSeparatorAt(actionContext(), anchorKind, anchorName);
    endInteraction();
}

// Creates one separator per distinct category and assigns every categorized mod to it.
void ModListWidget::groupByCategory()
{
    if (editsBlocked())
        return;
    const ActionContext context = actionContext();
    QStringList cats;
    QSet<QString> seen;
    for (const auto& m : m_mods) {
        QString c = m.category.trimmed();
        if (c.isEmpty() || seen.contains(c))
            continue;
        seen.insert(c);
        cats.append(c);
    }
    if (cats.isEmpty()) {
        dialogs::info(this, "Group by Category",
            "No mods have a category set. Assign categories first "
            "(right-click a mod → Set Category).");
        return;
    }

    if (!dialogs::confirm(this, "Group by Category",
        QString("Create separators for %1 distinct categor%2 and assign every "
                "categorized mod to its matching separator?\n\n"
                "Mods currently in a different separator will be reassigned. "
                "Mods with no category are left untouched.")
            .arg(cats.size()).arg(cats.size() == 1 ? "y" : "ies")))
        return;
    if (!matchesContext(context) || editsBlocked())
        return;
    for (const auto& meta : m_mods) {
        if (!meta.category.trimmed().isEmpty() && availableModIndex(context, meta.folder) < 0)
            return;
    }
    cats.clear();
    seen.clear();
    for (const auto& meta : m_mods) {
        const QString category = meta.category.trimmed();
        if (!category.isEmpty() && !seen.contains(category)) {
            seen.insert(category);
            cats.append(category);
        }
    }

    QSet<QString> existing;
    quint64 nextIdx = 0x10;
    for (const auto& s : m_separators) {
        existing.insert(s.name);
        quint64 v = parseHexIndex(s.visualIndex);
        if (v >= nextIdx) nextIdx = v + 0x10;
    }
    for (const auto& c : cats) {
        if (existing.contains(c))
            continue;
        SeparatorDef d;
        d.name = c;
        d.collapsed = false;
        d.visualIndex = formatHexIndex(nextIdx);
        nextIdx += 0x10;
        m_separators.push_back(d);
    }

    for (auto& meta : m_mods) {
        QString c = meta.category.trimmed();
        if (c.isEmpty() || meta.separator == c)
            continue;
        const QString yamlPath = metadataPathForFolder(meta.folder);
        if (yamlPath.isEmpty())
            continue;
        meta.separator = c;
        ModCatalog::patchMetadataField(yamlPath, "separator", c);
    }

    if (!m_visualMode) {
        m_visualMode = true;
        QSignalBlocker block(m_visualCheck);
        m_visualCheck->setChecked(true);
    }
    rebuildView();
    persistRowOrder();
}

}
