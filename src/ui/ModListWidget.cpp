#include "ModListWidget.h"
#include "ModListRowDelegate.h"
#include "ThemeManager.h"
#include "Dialogs.h"

#include <QApplication>
#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QDropEvent>
#include <QComboBox>
#include <QCheckBox>
#include <QPushButton>
#include <QMenu>
#include <QInputDialog>
#include <QDesktopServices>
#include <QUrl>
#include <QDir>
#include <QFile>
#include <QSet>
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

// Moves the multi-selected draggable rows to the computed target, then persists the new order.
void ModListTreeView::dropEvent(QDropEvent* event)
{
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

ModListWidget::ModListWidget(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
{
    auto* layout = new QVBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);

    auto* headerRow = new QHBoxLayout;
    auto* titleLabel = new QLabel("Mod List");
    titleLabel->setStyleSheet("font-weight: bold;");
    headerRow->addWidget(titleLabel);
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

void ModListWidget::loadForGame(const GameInfo& game)
{
    loadForGame(game, "Default");
}

void ModListWidget::loadForGame(const GameInfo& game, const QString& profileName)
{
    m_updatingModel = true;
    m_model->clear();
    m_mods.clear();
    m_activeGame = game;
    m_sortColumn = ModColPriority;
    m_sortOrder = Qt::AscendingOrder;
    m_view->header()->setSortIndicator(ModColPriority, Qt::AscendingOrder);
    m_updatingModel = false;

    if (!game.detected) {
        m_view->hide();
        m_placeholder->show();
        return;
    }

    m_gameId = game.shortName;
    m_profileName = profileName;

    m_modsDir = GameInfo::modsDirPathFor(m_gameId);

    m_placeholder->hide();
    m_view->show();

    scanModsFolder();
}

void ModListWidget::scanModsFolder()
{
    m_separators.clear();
    m_mods = ModCatalog::scan(m_modsDir);

    if (!m_gameId.isEmpty() && !m_profileName.isEmpty()) {
        std::vector<GrpcSeparator> seps;
        bool viewEnabled = false;
        QString err;
        if (m_grpc->listSeparators(m_gameId, m_profileName, seps, viewEnabled, err)) {
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

    rebuildView();

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
    dlg->resize(720, 480);
    auto* layout = new QVBoxLayout(dlg);

    auto buildSection = [&](const QString& heading, const QColor& accent,
                            const QList<QPair<QString, QString>>& rows,
                            const QString& emptyText, const QString& otherCol) {
        auto* lbl = new QLabel(QString("<b><span style='color:%2'>%1</span></b>")
                                   .arg(heading, accent.name()));
        lbl->setTextFormat(Qt::RichText);
        layout->addWidget(lbl);
        if (rows.isEmpty()) {
            auto* none = new QLabel(QString("<i>%1</i>").arg(emptyText));
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
    layout->addWidget(close, 0, Qt::AlignRight);
    dlg->show();
}

// Persists a checkbox toggle to metadata.yaml and pushes the full mod list to the daemon.
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

        bool enabled = r.checked;
        m_mods[modIdx].enabled = enabled;

        QString metaPath = m_modsDir + "/" + r.folder + "/metadata.yaml";
        ModCatalog::patchMetadataField(metaPath, "enabled", enabled ? "true" : "false");

        if (!m_gameId.isEmpty() && !m_profileName.isEmpty()) {
            std::vector<GrpcModListEntry> entries;
            if (!m_visualMode) {
                entries.reserve(m_model->rowCount());
                for (int rr = 0; rr < m_model->rowCount(); ++rr) {
                    const ModListRow& mr = m_model->rowAt(rr);
                    if (mr.kind != RowKindMod) continue;
                    GrpcModListEntry e;
                    e.modName = mr.folder;
                    if (e.modName.isEmpty()) e.modName = mr.name;
                    e.enabled = mr.checked;
                    e.priority = int(entries.size());
                    entries.push_back(std::move(e));
                }
            } else {
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
            }
            m_grpc->setModList(m_gameId, m_profileName, entries);
        }

        emit modToggled();
    }
}

void ModListWidget::onItemDoubleClicked(const QModelIndex& index)
{
    if (!index.isValid())
        return;

    const ModListRow& r = m_model->rowAt(index.row());
    if (r.kind == RowKindSeparator) {
        toggleCollapseAt(index.row());
        return;
    }

    if (index.column() != ModColCategory)
        return;

    int modIdx = r.modIndex;
    if (modIdx < 0 || modIdx >= int(m_mods.size()))
        return;

    QComboBox combo;
    combo.setEditable(true);
    combo.addItem("");
    combo.addItems(defaultCategories());
    combo.setCurrentText(r.category);

    QDialog dlg(m_view);
    dlg.setWindowTitle("Set Category");
    auto* dlgLayout = new QVBoxLayout(&dlg);
    dlgLayout->addWidget(new QLabel("Select or type a category:"));
    dlgLayout->addWidget(&combo);
    auto* okBtn = new QPushButton("OK");
    dlgLayout->addWidget(okBtn);
    connect(okBtn, &QPushButton::clicked, &dlg, &QDialog::accept);

    if (dlg.exec() == QDialog::Accepted)
        setCategoryForRow(modIdx, combo.currentText());
}

void ModListWidget::onContextMenu(const QPoint& pos)
{
    auto idx = m_view->indexAt(pos);
    int row = idx.isValid() ? idx.row() : -1;

    QMenu menu;

    if (row >= 0) {
        int kind = m_model->rowAt(row).kind;
        if (kind == RowKindSeparator) {
            menu.addAction("Toggle Collapse", [this, row] { toggleCollapseAt(row); });
            menu.addAction("Rename Separator...", [this, row] { renameSeparator(row); });
            menu.addSeparator();
            menu.addAction("Move to Top", [this, row] { moveSeparatorTo(row, true); });
            menu.addAction("Move to Bottom", [this, row] { moveSeparatorTo(row, false); });
            menu.addSeparator();
            menu.addAction("Remove Separator", [this, row] { removeSeparator(row); });
            menu.exec(m_view->viewport()->mapToGlobal(pos));
            return;
        }
        if (kind == RowKindOverwrite) {
            onOverwriteContextMenu(m_view->viewport()->mapToGlobal(pos));
            return;
        }
    }

    if (m_visualMode) {
        int insertAt = (row >= 0) ? row : m_model->rowCount();
        menu.addAction("Add Separator Here...", [this, insertAt] {
            createSeparatorAt(insertAt);
        });
        menu.addAction("Group by Category", [this] { groupByCategory(); });
        menu.addSeparator();
    }

    if (row < 0) {
        if (!menu.isEmpty())
            menu.exec(m_view->viewport()->mapToGlobal(pos));
        return;
    }
    const ModListRow& clicked = m_model->rowAt(row);
    if (clicked.kind != RowKindMod)
        return;
    int modIdx = clicked.modIndex;
    if (modIdx < 0 || modIdx >= int(m_mods.size()))
        return;
    const auto& meta = m_mods[modIdx];

    QList<int> selectedModIndexes;
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
            int idx = mr.modIndex;
            if (idx < 0 || idx >= int(m_mods.size()))
                continue;
            selectedModIndexes.append(idx);
            selectedFolders.append(m_mods[idx].folder);
            selectedNames.append(m_mods[idx].name);
        }
    }
    if (selectedModIndexes.size() >= 2) {
        QString summary = QString("%1 mods selected").arg(selectedModIndexes.size());
        auto* header = menu.addAction(summary);
        header->setEnabled(false);
        menu.addSeparator();

        menu.addAction("Enable All", [this, selectedModIndexes]() {
            for (int idx : selectedModIndexes) {
                int row = m_model->rowForModIndex(idx);
                if (row >= 0)
                    m_model->setData(m_model->index(row, ModColName), Qt::Checked,
                                     Qt::CheckStateRole);
            }
        });
        menu.addAction("Disable All", [this, selectedModIndexes]() {
            for (int idx : selectedModIndexes) {
                int row = m_model->rowForModIndex(idx);
                if (row >= 0)
                    m_model->setData(m_model->index(row, ModColName), Qt::Unchecked,
                                     Qt::CheckStateRole);
            }
        });
        menu.addSeparator();

        bool allReinstallable = true;
        for (int idx : selectedModIndexes) {
            if (m_mods[idx].sourceArchives.isEmpty()) {
                allReinstallable = false;
                break;
            }
        }
        auto* bulkReinstall = menu.addAction(QString("Reinstall %1 Mods").arg(selectedModIndexes.size()));
        bulkReinstall->setEnabled(allReinstallable);
        if (!allReinstallable)
            bulkReinstall->setToolTip("One or more selected mods have no source archives.");
        connect(bulkReinstall, &QAction::triggered, this,
                [this, selectedFolders, selectedNames]() {
            if (!dialogs::confirm(this, "Reinstall Mods",
                QString("Reinstall %1 mods by replaying their source archives?\n\n"
                        "Each mod's files will be cleared and re-extracted.")
                    .arg(selectedFolders.size())))
                return;
            int ok = 0, failed = 0;
            QStringList errors;
            for (int i = 0; i < selectedFolders.size(); ++i) {
                GrpcReinstallResult res;
                QString err;
                if (!m_grpc->reinstallMod(m_gameId, selectedFolders[i], res, err)) {
                    failed++;
                    errors.append(QString("• %1: %2").arg(selectedNames[i], err));
                } else {
                    ok++;
                }
            }
            scanModsFolder();
            if (failed > 0) {
                dialogs::warn(this, "Bulk Reinstall — Partial",
                    QString("Reinstalled %1, failed %2:\n\n%3").arg(ok).arg(failed).arg(errors.join("\n")));
            } else {
                dialogs::info(this, "Bulk Reinstall Complete",
                    QString("Reinstalled %1 mods.").arg(ok));
            }
        });
        menu.exec(m_view->viewport()->mapToGlobal(pos));
        return;
    }

    if (!meta.nexusUrl.isEmpty()) {
        menu.addAction("Visit Mod Page", [&meta] {
            QDesktopServices::openUrl(QUrl(meta.nexusUrl));
        });
    }
    menu.addAction(meta.nexusUrl.isEmpty() ? "Set Mod Page URL..." : "Change Mod Page URL...",
        [this, modIdx, &meta] {
            bool ok = false;
            QString url = QInputDialog::getText(m_view, "Mod Page URL",
                "Paste a URL (e.g. Nexus Mods page). Leave empty to clear.",
                QLineEdit::Normal, meta.nexusUrl, &ok);
            if (!ok)
                return;
            updateModPageUrl(modIdx, url.trimmed());
        });
    menu.addSeparator();

    menu.addAction("Show Conflicts...", [this, modName = meta.name] {
        showConflictDetailsForMod(modName);
    });

    menu.addAction("Open Mod Folder", [this, &meta] {
        QString path = m_modsDir + "/" + meta.folder;
        QDesktopServices::openUrl(QUrl::fromLocalFile(path));
    });

    auto* catMenu = menu.addMenu("Set Category");
    for (const auto& cat : defaultCategories()) {
        catMenu->addAction(cat, [this, modIdx, cat] { setCategoryForRow(modIdx, cat); });
    }
    catMenu->addSeparator();
    catMenu->addAction("Custom...", [this, modIdx] {
        bool ok = false;
        QString custom = QInputDialog::getText(m_view, "Custom Category",
            "Enter category name:", QLineEdit::Normal, "", &ok);
        if (ok && !custom.trimmed().isEmpty())
            setCategoryForRow(modIdx, custom.trimmed());
    });

    menu.addSeparator();

    {
        auto* reinstall = menu.addAction("Reinstall");
        bool haveArchives = !meta.sourceArchives.isEmpty();
        reinstall->setEnabled(haveArchives);
        if (haveArchives) {
            reinstall->setToolTip(
                QString("Replays %1 archive(s) in install order.").arg(meta.sourceArchives.size()));
            connect(reinstall, &QAction::triggered, this, [this, meta] {
                if (!dialogs::confirm(this, "Reinstall Mod",
                    QString("Reinstall \"%1\" by replaying %2 archive(s)?\n\n"
                            "The mod's files will be cleared and re-extracted "
                            "in the order they were installed.")
                        .arg(meta.name).arg(meta.sourceArchives.size())))
                    return;
                GrpcReinstallResult res;
                QString err;
                if (!m_grpc->reinstallMod(m_gameId, meta.folder, res, err)) {
                    dialogs::warn(this, "Reinstall Failed", err);
                    return;
                }
                if (res.archivesSkipped > 0) {
                    dialogs::info(this, "Reinstall Complete",
                        QString("Replayed %1, skipped %2 (missing archive). %3 files total.")
                            .arg(res.archivesReplayed).arg(res.archivesSkipped).arg(res.fileCount));
                }
                scanModsFolder();
            });
        } else {
            reinstall->setToolTip("No source archives recorded for this mod.");
        }
    }

    menu.addSeparator();

    menu.addAction("Rename Mod...", [this, &meta] {
        bool ok = false;
        QString newName = QInputDialog::getText(this, "Rename Mod",
            "New name (also becomes the folder name on disk):",
            QLineEdit::Normal, meta.folder, &ok);
        if (!ok || newName.isEmpty() || newName == meta.folder) return;
        QString err;
        if (!m_grpc->renameMod(m_gameId, meta.folder, newName, err)) {
            dialogs::warn(this, "Rename Failed", err);
            return;
        }
        scanModsFolder();
    });

    menu.addAction("Uninstall Mod", [this, &meta] {
        if (!dialogs::confirm(this, "Uninstall Mod",
            QString("Uninstall \"%1\"?\n\n"
                    "The mod folder will be removed and its archive will be "
                    "marked Uninstalled in the Downloads tab (the archive "
                    "itself is kept so you can reinstall later).")
                .arg(meta.name))) return;

        std::vector<QString> flagged;
        QString err;
        bool ok = m_grpc->uninstallMod(m_gameId, meta.folder, false, flagged, err);
        if (!ok && err.contains("mod_in_use:")) {
            QString profiles = err;
            int idx = profiles.indexOf("profiles=");
            profiles = idx >= 0 ? profiles.mid(idx + 9) : QString();
            if (!dialogs::confirm(this, "Mod In Use",
                QString("\"%1\" is enabled in profile(s): %2\n\n"
                        "Uninstall anyway? The mod will also be removed from "
                        "those profiles' mod lists.").arg(meta.name, profiles))) return;
            ok = m_grpc->uninstallMod(m_gameId, meta.folder, true, flagged, err);
        }
        if (!ok) {
            dialogs::warn(this, "Uninstall Failed", err);
            return;
        }
        scanModsFolder();
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

void ModListWidget::updateModPageUrl(int row, const QString& url)
{
    if (row < 0 || row >= static_cast<int>(m_mods.size()))
        return;
    QString folder = m_mods[row].folder;
    QString metaPath = m_modsDir + "/" + folder + "/metadata.yaml";
    QFile f(metaPath);
    if (!f.open(QIODevice::ReadOnly | QIODevice::Text))
        return;
    QString content = f.readAll();
    f.close();

    QStringList lines = content.split('\n');
    QStringList kept;
    kept.reserve(lines.size());
    for (const auto& ln : lines) {
        QString trimmed = ln.trimmed();
        if (trimmed.startsWith("mod_page:") || trimmed.startsWith("nexus_url:"))
            continue;
        kept.append(ln);
    }

    if (!url.isEmpty()) {
        int anchor = -1;
        for (int i = 0; i < kept.size(); ++i) {
            if (kept[i].trimmed() == "source_archives:") {
                anchor = i;
                break;
            }
        }
        QString newLine = QString("mod_page: \"%1\"").arg(url);
        if (anchor >= 0)
            kept.insert(anchor, newLine);
        else
            kept.append(newLine);
    }

    if (!f.open(QIODevice::WriteOnly | QIODevice::Text))
        return;
    f.write(kept.join('\n').toUtf8());
    f.close();

    m_mods[row].nexusUrl = url;
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
    m_visualMode = on;
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
        rebuildView();
        if (!wasVisual)
            persistSeparators();
    } else {
        m_visualCheck->setEnabled(true);
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

// Persists the current row order: true mode via setModList, visual mode via metadata index stamping.
void ModListWidget::persistRowOrder()
{
    if (m_updatingModel) return;

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
        m_grpc->setModList(m_gameId, m_profileName, entries);
        return;
    }

    QString currentSeparator;
    quint64 runningIdx = 0x10;
    std::vector<SeparatorDef> updatedSeparators;
    std::vector<GrpcModListEntry> collapsedEntries;

    for (int r = 0; r < m_model->rowCount(); ++r) {
        const ModListRow& row = m_model->rowAt(r);
        if (row.kind == RowKindSeparator) {
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
        auto& meta = m_mods[modIdx];
        QString newVisualIndex = formatHexIndex(runningIdx);
        QString newSeparator = currentSeparator;
        bool dirty = (meta.visualIndex != newVisualIndex) || (meta.separator != newSeparator);
        if (m_collapsedSeparatorView && meta.trueIndex != newVisualIndex)
            dirty = true;
        meta.visualIndex = newVisualIndex;
        meta.separator = newSeparator;
        if (m_collapsedSeparatorView)
            meta.trueIndex = newVisualIndex;
        if (dirty) {
            QString yamlPath = m_modsDir + "/" + meta.folder + "/metadata.yaml";
            ModCatalog::patchMetadataField(yamlPath, "visual_index", newVisualIndex);
            ModCatalog::patchMetadataField(yamlPath, "separator", newSeparator);
            if (m_collapsedSeparatorView)
                ModCatalog::patchMetadataField(yamlPath, "true_index", newVisualIndex);
        }
        if (m_collapsedSeparatorView) {
            GrpcModListEntry e;
            e.modName = meta.folder;
            e.enabled = meta.enabled;
            e.priority = int(collapsedEntries.size());
            collapsedEntries.push_back(std::move(e));
        }
        runningIdx += 0x10;
    }

    m_separators = updatedSeparators;
    persistSeparators();

    if (m_collapsedSeparatorView && !m_gameId.isEmpty() && !m_profileName.isEmpty())
        m_grpc->setModList(m_gameId, m_profileName, collapsedEntries);
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

void ModListWidget::createSeparatorAt(int visualRow)
{
    bool ok = false;
    QString name = QInputDialog::getText(m_view, "New Separator",
        "Separator name:", QLineEdit::Normal, "", &ok);
    name = name.trimmed();
    if (!ok || name.isEmpty())
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
    if (sepRow >= 0 && visualRow >= 0 && visualRow != sepRow)
        m_model->moveRowsTo({sepRow}, visualRow);
    persistRowOrder();
}

void ModListWidget::renameSeparator(int row)
{
    const ModListRow& r = m_model->rowAt(row);
    if (r.kind != RowKindSeparator) return;
    QString oldName = r.name;
    bool ok = false;
    QString newName = QInputDialog::getText(m_view, "Rename Separator",
        "New name:", QLineEdit::Normal, oldName, &ok);
    newName = newName.trimmed();
    if (!ok || newName.isEmpty() || newName == oldName) return;
    for (auto& meta : m_mods) {
        if (meta.separator == oldName) {
            meta.separator = newName;
            QString yamlPath = m_modsDir + "/" + meta.folder + "/metadata.yaml";
            ModCatalog::patchMetadataField(yamlPath, "separator", newName);
        }
    }
    for (auto& s : m_separators) {
        if (s.name == oldName) { s.name = newName; break; }
    }
    persistSeparators();
    rebuildView();
}

void ModListWidget::removeSeparator(int row)
{
    const ModListRow& r = m_model->rowAt(row);
    if (r.kind != RowKindSeparator) return;
    QString name = r.name;
    for (auto& meta : m_mods) {
        if (meta.separator == name) {
            meta.separator.clear();
            QString yamlPath = m_modsDir + "/" + meta.folder + "/metadata.yaml";
            ModCatalog::patchMetadataField(yamlPath, "separator", QString());
        }
    }
    m_separators.erase(std::remove_if(m_separators.begin(), m_separators.end(),
        [&](const SeparatorDef& s) { return s.name == name; }), m_separators.end());
    persistSeparators();
    rebuildView();
}

void ModListWidget::toggleCollapseAt(int row)
{
    const ModListRow& r = m_model->rowAt(row);
    if (r.kind != RowKindSeparator) return;
    QString name = r.name;
    for (auto& s : m_separators) {
        if (s.name == name) { s.collapsed = !s.collapsed; break; }
    }
    persistSeparators();
    rebuildView();
}

// Brackets the separator's index outside the current min/max so rebuildView sorts it to the desired end.
void ModListWidget::moveSeparatorTo(int row, bool toTop)
{
    const ModListRow& r = m_model->rowAt(row);
    if (r.kind != RowKindSeparator)
        return;
    QString name = r.name;
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

void ModListWidget::setCategoryForRow(int modIdx, const QString& category)
{
    if (modIdx < 0 || modIdx >= static_cast<int>(m_mods.size()))
        return;
    m_mods[modIdx].category = category;

    int modelRow = m_model->rowForModIndex(modIdx);
    if (modelRow >= 0)
        m_model->setCategoryAt(modelRow, category);

    QString metaPath = m_modsDir + "/" + m_mods[modIdx].folder + "/metadata.yaml";
    ModCatalog::patchMetadataField(metaPath, "category", category);
}

// Builds the right-click menu for the pinned Overwrite row.
void ModListWidget::onOverwriteContextMenu(const QPoint& globalPos)
{
    QMenu menu;

    std::vector<GrpcOverwriteEntry> files;
    QString owDir, err;
    bool ok = m_grpc->listOverwriteFiles(m_gameId, files, owDir, err);
    bool hasFiles = false;
    for (const auto& f : files) {
        if (!f.isDir) { hasFiles = true; break; }
    }

    auto* openAct = menu.addAction("Open Overwrite Folder");
    connect(openAct, &QAction::triggered, this, [this, owDir]() {
        QString dir = owDir;
        if (dir.isEmpty()) {
            dir = m_modsDir + "/" + kOverwriteModName;
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
        extractAll->setToolTip(QString("Daemon not reachable: %1").arg(err));
    connect(extractAll, &QAction::triggered, this, &ModListWidget::extractOverwriteAll);

    auto* extractSel = menu.addAction("Extract Selected Files to New Mod...");
    extractSel->setEnabled(ok && hasFiles);
    connect(extractSel, &QAction::triggered, this, &ModListWidget::extractOverwriteSelected);

    menu.exec(globalPos);
}

void ModListWidget::extractOverwriteAll()
{
    bool ok = false;
    QString name = QInputDialog::getText(this, "Extract Overwrite",
        "New mod name (empty list will extract every file in Overwrite):",
        QLineEdit::Normal, "Overwrite Snapshot", &ok);
    if (!ok || name.trimmed().isEmpty())
        return;
    int count = 0;
    QString err;
    if (!m_grpc->extractOverwriteToMod(m_gameId, name.trimmed(), {}, false, count, err)) {
        dialogs::warn(this, "Extract Failed", err);
        return;
    }
    dialogs::info(this, "Extract Complete",
        QString("Moved %1 file(s) into mod \"%2\".").arg(count).arg(name.trimmed()));
    scanModsFolder();
}

// Pops a multi-select picker so the user can graduate a subset of Overwrite into a named mod folder.
void ModListWidget::extractOverwriteSelected()
{
    std::vector<GrpcOverwriteEntry> files;
    QString owDir, err;
    if (!m_grpc->listOverwriteFiles(m_gameId, files, owDir, err)) {
        dialogs::warn(this, "Extract Failed", err);
        return;
    }

    QDialog dlg(this);
    dlg.setWindowTitle("Extract Files from Overwrite");
    dlg.resize(720, 520);
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

    if (dlg.exec() != QDialog::Accepted)
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
    if (!m_grpc->extractOverwriteToMod(m_gameId, name, chosen, keepCb->isChecked(),
                                        count, rpcErr)) {
        dialogs::warn(this, "Extract Failed", rpcErr);
        return;
    }
    dialogs::info(this, "Extract Complete",
        QString("Moved %1 file(s) into mod \"%2\".").arg(count).arg(name));
    scanModsFolder();
}

void ModListWidget::onAddSeparatorClicked()
{
    if (m_view->isHidden())
        return;
    if (!m_visualMode)
        m_visualCheck->setChecked(true);
    bool atTop = QApplication::keyboardModifiers().testFlag(Qt::ShiftModifier);
    int targetRow = atTop ? 0 : (m_model->rowCount() - 1);
    if (targetRow < 0)
        targetRow = 0;
    createSeparatorAt(targetRow);
}

// Creates one separator per distinct category and assigns every categorized mod to it.
void ModListWidget::groupByCategory()
{
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
        meta.separator = c;
        QString yamlPath = m_modsDir + "/" + meta.folder + "/metadata.yaml";
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
