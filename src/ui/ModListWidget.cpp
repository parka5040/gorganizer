#include "ModListWidget.h"

#include <QApplication>
#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QDropEvent>
#include <QComboBox>
#include <QCheckBox>
#include <QMessageBox>
#include <QPushButton>
#include <QMenu>
#include <QInputDialog>
#include <QDesktopServices>
#include <QUrl>
#include <QDir>
#include <QFile>
#include <QTextStream>
#include <QRegularExpression>
#include <QBrush>
#include <QColor>
#include <QFont>
#include <QSet>
#include <QTreeWidget>
#include <QDialog>
#include <QListWidget>
#include <QLineEdit>
#include <QDialogButtonBox>
#include <algorithm>

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

int ModListTreeView::dropTargetRow(QDropEvent* event) const
{
    auto pos = event->position().toPoint();
    auto idx = indexAt(pos);

    if (!idx.isValid())
        return model()->rowCount();

    // Dropping anywhere on a separator row = "drop into this separator":
    // place the dragged mod immediately below the separator so the
    // walking-cursor in persistRowOrder records it as a member.
    if (auto* m = static_cast<const QStandardItemModel*>(model())) {
        auto* it = m->item(idx.row(), ModColName);
        if (it && it->data(Qt::UserRole + 50).toInt() == RowKindSeparator)
            return idx.row() + 1;
    }

    auto rect = visualRect(idx);
    bool aboveHalf = (pos.y() < rect.center().y());
    return aboveHalf ? idx.row() : idx.row() + 1;
}

void ModListTreeView::dropEvent(QDropEvent* event)
{
    if (!model())
        return;

    if (!(m_owner->m_sortColumn == ModColPriority && m_owner->m_sortOrder == Qt::AscendingOrder)) {
        event->ignore();
        return;
    }

    auto* m = static_cast<QStandardItemModel*>(model());
    int count = m->rowCount();

    // Multi-select drag: collect every selected row that is draggable
    // (mods and separators). The Overwrite pseudo-row is silently
    // dropped from the set rather than rejecting the whole gesture.
    QList<int> srcRows;
    {
        QSet<int> seen;
        for (const auto& idx : selectionModel()->selectedRows(ModColName)) {
            int r = idx.row();
            if (r < 0 || r >= count || seen.contains(r))
                continue;
            auto* it = m->item(r, ModColName);
            if (!it)
                continue;
            if (it->data(Qt::UserRole + 50).toInt() == RowKindOverwrite)
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
    if (destRow < 0 || destRow > count) {
        event->ignore();
        return;
    }
    // Drops past the bottom map onto the Overwrite row, which is illegal.
    if (destRow >= count) {
        event->ignore();
        return;
    }
    if (auto* destItem = m->item(destRow, ModColName);
        destItem && destItem->data(Qt::UserRole + 50).toInt() == RowKindOverwrite) {
        event->ignore();
        return;
    }

    // Where the block lands once the source rows are pulled out.
    int landingRow = destRow;
    for (int r : srcRows)
        if (r < destRow) landingRow--;

    QVector<QList<QStandardItem*>> taken(srcRows.size());
    for (int i = srcRows.size() - 1; i >= 0; --i)
        taken[i] = m->takeRow(srcRows[i]);

    for (int i = 0; i < taken.size(); ++i)
        m->insertRow(landingRow + i, taken[i]);

    m_owner->recalculatePriorities();
    m_owner->persistRowOrder();

    auto* sel = selectionModel();
    sel->clearSelection();
    QItemSelection range;
    int lastCol = m->columnCount() - 1;
    for (int i = 0; i < taken.size(); ++i) {
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

// Simple line-based YAML parser for metadata.yaml — no external library required.
ModMetadata ModListWidget::readMetadata(const QString& yamlPath)
{
    ModMetadata meta;
    QFile f(yamlPath);
    if (!f.open(QIODevice::ReadOnly | QIODevice::Text))
        return meta;

    auto stripQuotes = [](QString s) {
        s = s.trimmed();
        if (s.startsWith('"') && s.endsWith('"'))
            s = s.mid(1, s.length() - 2);
        return s;
    };

    QTextStream in(&f);
    bool inSourceList = false;
    while (!in.atEnd()) {
        QString raw = in.readLine();
        QString line = raw.trimmed();
        if (line.startsWith('#') || line.isEmpty())
            continue;

        if (!raw.startsWith(' ') && line.endsWith(':')) {
            QString section = line.left(line.length() - 1).trimmed();
            inSourceList = (section == "source_archives");
            continue;
        }

        if (inSourceList) {
            if (line.startsWith("- path:")) {
                meta.sourceArchives.append(stripQuotes(line.mid(QString("- path:").length())));
            } else if (line.startsWith("path:")) {
                meta.sourceArchives.append(stripQuotes(line.mid(QString("path:").length())));
            } else if (line.startsWith("- ")) {
                QString rest = line.mid(2);
                if (!rest.contains(':'))
                    meta.sourceArchives.append(stripQuotes(rest));
            }
            continue;
        }

        int colon = line.indexOf(':');
        if (colon < 0)
            continue;
        QString key = line.left(colon).trimmed();
        QString val = stripQuotes(line.mid(colon + 1));

        if (key == "name")            meta.name = val;
        else if (key == "folder")     meta.folder = val;
        else if (key == "installed")  meta.installed = val;
        else if (key == "source_archive") meta.sourceArchive = val;
        else if (key == "nexus_url")  meta.nexusUrl = val;
        else if (key == "mod_page")   meta.nexusUrl = val;
        else if (key == "category")   meta.category = val;
        else if (key == "version")    meta.version = val;
        else if (key == "enabled")    meta.enabled = (val == "true");
        else if (key == "file_count") meta.fileCount = val.toInt();
        else if (key == "true_index")   meta.trueIndex = val;
        else if (key == "visual_index") meta.visualIndex = val;
        else if (key == "separator")    meta.separator = val;
    }

    if (meta.sourceArchives.isEmpty() && !meta.sourceArchive.isEmpty())
        meta.sourceArchives.append(meta.sourceArchive);

    return meta;
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

    m_model = new QStandardItemModel(this);
    m_model->setHorizontalHeaderLabels({"Priority", "Conflicts", "Mod Name", "Category", "Version"});

    m_view = new ModListTreeView(this);
    m_view->setModel(m_model);
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

    connect(m_view->header(), &QHeaderView::sectionClicked, this, &ModListWidget::onHeaderClicked);
    connect(m_grpc, &GrpcClient::conflictsReceived, this, &ModListWidget::onConflictsReceived);
    connect(m_model, &QStandardItemModel::dataChanged, this, &ModListWidget::onModelDataChanged);
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
    placeholderLabel->setStyleSheet("color: gray;");
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
    m_model->removeRows(0, m_model->rowCount());
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

    static const QHash<QString, QString> dirNames = {
        {"morrowind", "Morrowind_Mods"}, {"oblivion", "Oblivion_Mods"},
        {"skyrim", "Skyrim_Mods"}, {"skyrimse", "SkyrimSE_Mods"},
        {"fallout3", "Fallout3_Mods"}, {"falloutnv", "FalloutNV_Mods"},
        {"fallout4", "Fallout4_Mods"}, {"starfield", "Starfield_Mods"},
        {"ttw", "TTW_Mods"},
    };
    QByteArray root = qgetenv("GORGANIZER_ROOT");
    if (!root.isEmpty()) {
        QString name = dirNames.value(m_gameId, m_gameId + "_Mods");
        m_modsDir = QString::fromUtf8(root) + "/" + name;
    } else {
        QString dataHome = qEnvironmentVariable("XDG_DATA_HOME");
        if (dataHome.isEmpty())
            dataHome = QDir::homePath() + "/.local/share";
        m_modsDir = dataHome + "/gorganizer/" + m_gameId + "/mods";
    }

    m_placeholder->hide();
    m_view->show();

    scanModsFolder();
}

void ModListWidget::scanModsFolder()
{
    m_mods.clear();
    m_separators.clear();

    QDir modsDir(m_modsDir);
    if (modsDir.exists()) {
        auto entries = modsDir.entryList(QDir::Dirs | QDir::NoDotAndDotDot, QDir::Name);
        for (const auto& dirName : entries) {
            if (dirName == "Downloads" || dirName.startsWith('.'))
                continue;
            if (dirName == kOverwriteModName)
                continue;
            QString metaPath = m_modsDir + "/" + dirName + "/metadata.yaml";
            ModMetadata meta;
            if (QFile::exists(metaPath)) {
                meta = readMetadata(metaPath);
            } else {
                meta.name = dirName;
                meta.folder = dirName;
                meta.enabled = true;
            }
            if (meta.folder.isEmpty())
                meta.folder = dirName;
            if (meta.name.isEmpty())
                meta.name = dirName;

            m_mods.push_back(meta);
        }
    }

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
            m_visualMode = viewEnabled;
            QSignalBlocker block(m_visualCheck);
            m_visualCheck->setChecked(viewEnabled);
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

    for (int row = 0; row < m_model->rowCount(); ++row) {
        auto* nameItem = m_model->item(row, ModColName);
        auto* conflictItem = m_model->item(row, ModColConflicts);
        if (!nameItem || !conflictItem) continue;

        QString modName = nameItem->text();
        int wins = winCounts.value(modName, 0);
        int losses = loseCounts.value(modName, 0);

        conflictItem->setText("");
        conflictItem->setToolTip("");
        conflictItem->setForeground(QBrush());

        if (wins > 0 && losses > 0) {
            conflictItem->setText("+-");
            conflictItem->setToolTip(QString("Overwrites %1 file(s), overwritten in %2 file(s) "
                                             "— right-click for details").arg(wins).arg(losses));
            conflictItem->setForeground(QColor(255, 165, 0));
        } else if (wins > 0) {
            conflictItem->setText("+");
            conflictItem->setToolTip(QString("Overwrites %1 file(s) — right-click for details").arg(wins));
            conflictItem->setForeground(QColor(100, 200, 100));
        } else if (losses > 0) {
            conflictItem->setText("-");
            conflictItem->setToolTip(QString("Overwritten in %1 file(s) — right-click for details").arg(losses));
            conflictItem->setForeground(QColor(255, 100, 100));
        }
    }

    repaintConflictHighlights();
}

void ModListWidget::onSelectionChanged()
{
    repaintConflictHighlights();
}

// Paints row backgrounds red/green to show what the single-selected mod overwrites or is overwritten by.
void ModListWidget::repaintConflictHighlights()
{
    for (int row = 0; row < m_model->rowCount(); ++row) {
        for (int col = 0; col < m_model->columnCount(); ++col) {
            if (auto* it = m_model->item(row, col))
                it->setBackground(QBrush());
        }
    }

    auto rows = m_view->selectionModel()->selectedRows();
    if (rows.size() != 1)
        return;

    int selRow = rows.first().row();
    auto* selName = m_model->item(selRow, ModColName);
    if (!selName || selName->data(Qt::UserRole + 50).toInt() != RowKindMod)
        return;
    QString selectedMod = selName->text();

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

    QBrush redBrush(QColor(220, 80, 80, 100));
    QBrush greenBrush(QColor(80, 180, 100, 100));

    for (int row = 0; row < m_model->rowCount(); ++row) {
        if (row == selRow)
            continue;
        auto* nameItem = m_model->item(row, ModColName);
        if (!nameItem || nameItem->data(Qt::UserRole + 50).toInt() != RowKindMod)
            continue;
        QString name = nameItem->text();
        QBrush b;
        if (loserOf.contains(name))
            b = redBrush;
        else if (winnerOver.contains(name))
            b = greenBrush;
        else
            continue;
        for (int col = 0; col < m_model->columnCount(); ++col) {
            if (auto* it = m_model->item(row, col))
                it->setBackground(b);
        }
    }
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
                 QColor(80, 180, 100), winsOver,
                 "This mod doesn't overwrite any files.", "Loser");
    buildSection(QString("Overwritten in %1 file(s):").arg(overwrittenBy.size()),
                 QColor(220, 80, 80), overwrittenBy,
                 "This mod isn't overwritten by any other mod.", "Winner");

    auto* close = new QPushButton("Close");
    connect(close, &QPushButton::clicked, dlg, &QDialog::accept);
    layout->addWidget(close, 0, Qt::AlignRight);
    dlg->show();
}

void ModListWidget::onModelDataChanged(const QModelIndex& topLeft, const QModelIndex&,
                                       const QList<int>& roles)
{
    if (m_updatingModel)
        return;

    if ((roles.contains(Qt::CheckStateRole) || roles.isEmpty()) && topLeft.column() == ModColName) {
        int row = topLeft.row();
        auto* item = m_model->item(row, ModColName);
        if (!item) return;
        if (item->data(Qt::UserRole + 50).toInt() != RowKindMod) return;
        int modIdx = item->data(Qt::UserRole + 101).toInt();
        if (modIdx < 0 || modIdx >= int(m_mods.size())) return;

        bool enabled = (item->checkState() == Qt::Checked);
        m_mods[modIdx].enabled = enabled;

        QString folder = item->data(Qt::UserRole + 100).toString();
        QString metaPath = m_modsDir + "/" + folder + "/metadata.yaml";
        patchMetadataField(metaPath, "enabled", enabled ? "true" : "false");

        if (!m_gameId.isEmpty() && !m_profileName.isEmpty()) {
            std::vector<GrpcModListEntry> entries;
            if (!m_visualMode) {
                entries.reserve(m_model->rowCount());
                for (int r = 0; r < m_model->rowCount(); ++r) {
                    auto* nameItem = m_model->item(r, ModColName);
                    if (!nameItem) continue;
                    if (nameItem->data(Qt::UserRole + 50).toInt() != RowKindMod) continue;
                    GrpcModListEntry e;
                    e.modName = nameItem->data(Qt::UserRole + 100).toString();
                    if (e.modName.isEmpty()) e.modName = nameItem->text();
                    e.enabled = (nameItem->checkState() == Qt::Checked);
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

    auto* nameItem = m_model->item(index.row(), ModColName);
    if (nameItem && nameItem->data(Qt::UserRole + 50).toInt() == RowKindSeparator) {
        toggleCollapseAt(index.row());
        return;
    }

    if (index.column() != ModColCategory)
        return;

    auto* item = m_model->item(index.row(), ModColCategory);
    if (!item)
        return;

    int modIdx = nameItem ? nameItem->data(Qt::UserRole + 101).toInt() : -1;
    if (modIdx < 0 || modIdx >= int(m_mods.size()))
        return;

    QComboBox combo;
    combo.setEditable(true);
    combo.addItem("");
    combo.addItems(defaultCategories());
    combo.setCurrentText(item->text());

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
        auto* nameItem = m_model->item(row, ModColName);
        int kind = nameItem ? nameItem->data(Qt::UserRole + 50).toInt() : RowKindMod;
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
    auto* nameItem = m_model->item(row, ModColName);
    if (!nameItem || nameItem->data(Qt::UserRole + 50).toInt() != RowKindMod)
        return;
    int modIdx = nameItem->data(Qt::UserRole + 101).toInt();
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
            auto* it = m_model->item(r, ModColName);
            if (!it || it->data(Qt::UserRole + 50).toInt() != RowKindMod)
                continue;
            int idx = it->data(Qt::UserRole + 101).toInt();
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
                if (idx >= 0 && idx < int(m_mods.size())) {
                    int row = -1;
                    for (int r = 0; r < m_model->rowCount(); ++r) {
                        auto* it = m_model->item(r, ModColName);
                        if (it && it->data(Qt::UserRole + 101).toInt() == idx) { row = r; break; }
                    }
                    if (row >= 0) {
                        auto* it = m_model->item(row, ModColName);
                        if (it && it->isCheckable()) it->setCheckState(Qt::Checked);
                    }
                }
            }
        });
        menu.addAction("Disable All", [this, selectedModIndexes]() {
            for (int idx : selectedModIndexes) {
                if (idx >= 0 && idx < int(m_mods.size())) {
                    int row = -1;
                    for (int r = 0; r < m_model->rowCount(); ++r) {
                        auto* it = m_model->item(r, ModColName);
                        if (it && it->data(Qt::UserRole + 101).toInt() == idx) { row = r; break; }
                    }
                    if (row >= 0) {
                        auto* it = m_model->item(row, ModColName);
                        if (it && it->isCheckable()) it->setCheckState(Qt::Unchecked);
                    }
                }
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
            auto reply = QMessageBox::question(this, "Reinstall Mods",
                QString("Reinstall %1 mods by replaying their source archives?\n\n"
                        "Each mod's files will be cleared and re-extracted.")
                    .arg(selectedFolders.size()),
                QMessageBox::Yes | QMessageBox::No);
            if (reply != QMessageBox::Yes)
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
                QMessageBox::warning(this, "Bulk Reinstall — Partial",
                    QString("Reinstalled %1, failed %2:\n\n%3").arg(ok).arg(failed).arg(errors.join("\n")));
            } else {
                QMessageBox::information(this, "Bulk Reinstall Complete",
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
                auto reply = QMessageBox::question(this, "Reinstall Mod",
                    QString("Reinstall \"%1\" by replaying %2 archive(s)?\n\n"
                            "The mod's files will be cleared and re-extracted "
                            "in the order they were installed.")
                        .arg(meta.name).arg(meta.sourceArchives.size()),
                    QMessageBox::Yes | QMessageBox::No);
                if (reply != QMessageBox::Yes)
                    return;
                GrpcReinstallResult res;
                QString err;
                if (!m_grpc->reinstallMod(m_gameId, meta.folder, res, err)) {
                    QMessageBox::warning(this, "Reinstall Failed", err);
                    return;
                }
                if (res.archivesSkipped > 0) {
                    QMessageBox::information(this, "Reinstall Complete",
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
            QMessageBox::warning(this, "Rename Failed", err);
            return;
        }
        scanModsFolder();
    });

    menu.addAction("Uninstall Mod", [this, &meta] {
        auto reply = QMessageBox::question(this, "Uninstall Mod",
            QString("Uninstall \"%1\"?\n\n"
                    "The mod folder will be removed and its archive will be "
                    "marked Uninstalled in the Downloads tab (the archive "
                    "itself is kept so you can reinstall later).")
                .arg(meta.name),
            QMessageBox::Yes | QMessageBox::No);
        if (reply != QMessageBox::Yes) return;

        std::vector<QString> flagged;
        QString err;
        bool ok = m_grpc->uninstallMod(m_gameId, meta.folder, /*force=*/false, flagged, err);
        if (!ok && err.contains("mod_in_use:")) {
            QString profiles = err;
            int idx = profiles.indexOf("profiles=");
            profiles = idx >= 0 ? profiles.mid(idx + 9) : QString();
            auto confirm = QMessageBox::question(this, "Mod In Use",
                QString("\"%1\" is enabled in profile(s): %2\n\n"
                        "Uninstall anyway? The mod will also be removed from "
                        "those profiles' mod lists.").arg(meta.name, profiles),
                QMessageBox::Yes | QMessageBox::No);
            if (confirm != QMessageBox::Yes) return;
            ok = m_grpc->uninstallMod(m_gameId, meta.folder, /*force=*/true, flagged, err);
        }
        if (!ok) {
            QMessageBox::warning(this, "Uninstall Failed", err);
            return;
        }
        scanModsFolder();
    });

    menu.exec(m_view->viewport()->mapToGlobal(pos));
}

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
        applySort(m_sortColumn, m_sortOrder);
    }
}

void ModListWidget::applySort(int column, Qt::SortOrder order)
{
    struct RowData {
        QList<QStandardItem*> items;
        int priority;
        QString text;
    };

    QVector<RowData> rows;
    rows.reserve(m_model->rowCount());
    while (m_model->rowCount() > 0) {
        auto items = m_model->takeRow(0);
        int pri = items[ModColPriority]->data(Qt::UserRole).toInt();
        QString sortKey = items[column]->text();
        rows.append({items, pri, sortKey});
    }

    if (column == ModColPriority) {
        std::sort(rows.begin(), rows.end(), [order](const RowData& a, const RowData& b) {
            return order == Qt::AscendingOrder ? a.priority < b.priority : a.priority > b.priority;
        });
    } else {
        std::sort(rows.begin(), rows.end(), [order](const RowData& a, const RowData& b) {
            int cmp = a.text.compare(b.text, Qt::CaseInsensitive);
            return order == Qt::AscendingOrder ? cmp < 0 : cmp > 0;
        });
    }

    for (auto& rd : rows)
        m_model->appendRow(rd.items);
}

void ModListWidget::restorePriorityOrder()
{
    if (m_visualMode) {
        rebuildView();
        return;
    }

    struct RowData {
        QList<QStandardItem*> items;
        int priority;
    };

    QVector<RowData> rows;
    rows.reserve(m_model->rowCount());
    while (m_model->rowCount() > 0) {
        auto items = m_model->takeRow(0);
        int pri = items[ModColPriority]->data(Qt::UserRole).toInt();
        rows.append({items, pri});
    }

    std::sort(rows.begin(), rows.end(), [](const RowData& a, const RowData& b) {
        return a.priority < b.priority;
    });

    for (auto& rd : rows)
        m_model->appendRow(rd.items);
}

void ModListWidget::recalculatePriorities()
{
    int p = 0;
    for (int i = 0; i < m_model->rowCount(); ++i) {
        auto* item = m_model->item(i, ModColPriority);
        auto* nameItem = m_model->item(i, ModColName);
        if (!item || !nameItem) continue;
        int kind = nameItem->data(Qt::UserRole + 50).toInt();
        if (kind == RowKindSeparator || kind == RowKindOverwrite) {
            item->setData(-1, Qt::UserRole);
            continue;
        }
        item->setText(QString::number(p));
        item->setData(p, Qt::UserRole);
        ++p;
    }
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
} // anonymous namespace

void ModListWidget::onVisualToggled(bool on)
{
    m_visualMode = on;
    rebuildView();
    persistSeparators();
}

void ModListWidget::rebuildView()
{
    m_updatingModel = true;
    m_model->removeRows(0, m_model->rowCount());

    struct Row {
        QList<QStandardItem*> items;
        VisualKey vk;
        ModRowKind kind;
        int modIndex = -1;
        int separatorIndex = -1;
    };
    std::vector<Row> rows;

    auto makeModRow = [&](int modIdx) -> Row {
        const auto& meta = m_mods[modIdx];
        auto* priorityItem = new QStandardItem;
        priorityItem->setEditable(false);
        priorityItem->setTextAlignment(Qt::AlignCenter);
        priorityItem->setData(int(RowKindMod), Qt::UserRole + 50);

        auto* conflictItem = new QStandardItem;
        conflictItem->setEditable(false);
        conflictItem->setTextAlignment(Qt::AlignCenter);

        auto* nameItem = new QStandardItem(meta.name);
        nameItem->setCheckable(true);
        nameItem->setCheckState(meta.enabled ? Qt::Checked : Qt::Unchecked);
        nameItem->setEditable(false);
        nameItem->setData(meta.folder, Qt::UserRole + 100);
        nameItem->setData(int(RowKindMod), Qt::UserRole + 50);
        nameItem->setData(modIdx, Qt::UserRole + 101);

        auto* categoryItem = new QStandardItem(meta.category);
        categoryItem->setEditable(false);
        auto* versionItem = new QStandardItem(meta.version);
        versionItem->setEditable(false);

        Row r;
        r.items = {priorityItem, conflictItem, nameItem, categoryItem, versionItem};
        r.kind = RowKindMod;
        r.modIndex = modIdx;
        return r;
    };

    auto makeSeparatorRow = [&](int sepIdx) -> Row {
        const auto& sep = m_separators[sepIdx];
        auto* priorityItem = new QStandardItem(sep.collapsed ? "▶" : "▼");
        priorityItem->setEditable(false);
        priorityItem->setTextAlignment(Qt::AlignCenter);
        priorityItem->setData(int(RowKindSeparator), Qt::UserRole + 50);

        auto* conflictItem = new QStandardItem;
        conflictItem->setEditable(false);

        auto* nameItem = new QStandardItem(sep.name);
        nameItem->setEditable(false);
        nameItem->setData(int(RowKindSeparator), Qt::UserRole + 50);
        nameItem->setData(sep.name, Qt::UserRole + 102);
        QFont f = nameItem->font();
        f.setBold(true);
        f.setItalic(true);
        nameItem->setFont(f);
        nameItem->setForeground(QBrush(QColor(200, 200, 120)));
        QBrush bg(QColor(60, 60, 80));
        priorityItem->setBackground(bg);
        conflictItem->setBackground(bg);
        nameItem->setBackground(bg);

        auto* categoryItem = new QStandardItem;
        categoryItem->setEditable(false);
        categoryItem->setBackground(bg);
        auto* versionItem = new QStandardItem;
        versionItem->setEditable(false);
        versionItem->setBackground(bg);

        Row r;
        r.items = {priorityItem, conflictItem, nameItem, categoryItem, versionItem};
        r.kind = RowKindSeparator;
        r.separatorIndex = sepIdx;
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
            rows.push_back(makeModRow(i));
    } else {
        QHash<QString, quint64> sepRank;
        QHash<QString, bool> sepCollapsed;
        for (const auto& s : m_separators) {
            sepRank[s.name] = parseHexIndex(s.visualIndex);
            sepCollapsed[s.name] = s.collapsed;
        }

        for (int i = 0; i < int(m_separators.size()); ++i) {
            Row r = makeSeparatorRow(i);
            r.vk.sepIdx = parseHexIndex(m_separators[i].visualIndex);
            r.vk.kind = 0;
            rows.push_back(std::move(r));
        }
        for (int i = 0; i < int(m_mods.size()); ++i) {
            const auto& m = m_mods[i];
            quint64 sepIdx = 0;
            if (!m.separator.isEmpty() && sepRank.contains(m.separator))
                sepIdx = sepRank[m.separator];
            quint64 own = parseHexIndex(m.visualIndex);
            if (own == 0) own = parseHexIndex(m.trueIndex);

            if (!m.separator.isEmpty() && sepCollapsed.value(m.separator, false))
                continue;

            Row r = makeModRow(i);
            r.vk.sepIdx = sepIdx;
            r.vk.kind = 1;
            r.vk.own = own;
            r.vk.stableTie = i;
            rows.push_back(std::move(r));
        }
        std::stable_sort(rows.begin(), rows.end(),
            [](const Row& a, const Row& b) { return a.vk < b.vk; });
    }

    int displayIndex = 0;
    for (auto& r : rows) {
        if (r.kind == RowKindMod) {
            r.items[ModColPriority]->setText(QString::number(displayIndex));
            r.items[ModColPriority]->setData(displayIndex, Qt::UserRole);
            ++displayIndex;
        } else {
            r.items[ModColPriority]->setData(-1, Qt::UserRole);
        }
        m_model->appendRow(r.items);
    }

    appendOverwriteRow();

    m_updatingModel = false;
}

// Pins the always-on Overwrite layer at the bottom of the table as a row-spanned pseudo-row.
void ModListWidget::appendOverwriteRow()
{
    auto* spanItem = new QStandardItem(QString("— Overwrite —"));
    spanItem->setEditable(false);
    spanItem->setSelectable(false);
    spanItem->setDragEnabled(false);
    spanItem->setDropEnabled(false);
    spanItem->setData(int(RowKindOverwrite), Qt::UserRole + 50);
    spanItem->setData(QString(kOverwriteModName), Qt::UserRole + 100);
    spanItem->setData(-1, Qt::UserRole);
    spanItem->setTextAlignment(Qt::AlignCenter);
    QFont f = spanItem->font();
    f.setItalic(true);
    spanItem->setFont(f);
    spanItem->setForeground(QBrush(QColor(170, 170, 200)));
    spanItem->setToolTip(
        "Always-on write-capture layer.\n"
        "Loose .esp/.dds/.bsa files dropped here are visible in-game at the\n"
        "highest priority. Right-click to extract into a real mod folder.");

    auto* col2 = new QStandardItem;  col2->setEditable(false);  col2->setData(int(RowKindOverwrite), Qt::UserRole + 50);
    auto* col3 = new QStandardItem;  col3->setEditable(false);  col3->setData(int(RowKindOverwrite), Qt::UserRole + 50);
    auto* col4 = new QStandardItem;  col4->setEditable(false);  col4->setData(int(RowKindOverwrite), Qt::UserRole + 50);
    auto* col5 = new QStandardItem;  col5->setEditable(false);  col5->setData(int(RowKindOverwrite), Qt::UserRole + 50);

    QList<QStandardItem*> items{spanItem, col2, col3, col4, col5};
    m_model->appendRow(items);

    int row = m_model->rowCount() - 1;
    m_view->setFirstColumnSpanned(row, QModelIndex(), true);
}

void ModListWidget::patchMetadataField(const QString& yamlPath, const QString& key,
                                        const QString& value)
{
    QFile f(yamlPath);
    if (!f.open(QIODevice::ReadOnly | QIODevice::Text))
        return;
    QString content = f.readAll();
    f.close();

    QStringList lines = content.split('\n');
    QStringList kept;
    bool matched = false;
    const QString prefix = key + ":";
    for (const auto& ln : lines) {
        QString trimmed = ln.trimmed();
        if (!trimmed.startsWith(prefix)) {
            kept.append(ln);
            continue;
        }
        if (!ln.startsWith(' ') && !ln.startsWith('\t')) {
            matched = true;
            if (!value.isEmpty())
                kept.append(QString("%1: \"%2\"").arg(key, value));
        } else {
            kept.append(ln);
        }
    }
    if (!matched && !value.isEmpty()) {
        int anchor = -1;
        for (int i = 0; i < kept.size(); ++i) {
            if (kept[i].trimmed() == "source_archives:") { anchor = i; break; }
        }
        QString newLine = QString("%1: \"%2\"").arg(key, value);
        if (anchor >= 0)
            kept.insert(anchor, newLine);
        else
            kept.append(newLine);
    }
    if (!f.open(QIODevice::WriteOnly | QIODevice::Text))
        return;
    f.write(kept.join('\n').toUtf8());
    f.close();
}

void ModListWidget::persistRowOrder()
{
    if (m_updatingModel) return;

    if (!m_visualMode) {
        if (m_gameId.isEmpty() || m_profileName.isEmpty())
            return;
        std::vector<GrpcModListEntry> entries;
        for (int r = 0; r < m_model->rowCount(); ++r) {
            auto* nameItem = m_model->item(r, ModColName);
            if (!nameItem) continue;
            if (nameItem->data(Qt::UserRole + 50).toInt() != RowKindMod) continue;
            GrpcModListEntry e;
            e.modName = nameItem->data(Qt::UserRole + 100).toString();
            if (e.modName.isEmpty()) e.modName = nameItem->text();
            e.enabled = (nameItem->checkState() == Qt::Checked);
            e.priority = r;
            entries.push_back(std::move(e));
        }
        m_grpc->setModList(m_gameId, m_profileName, entries);
        return;
    }

    QString currentSeparator;
    quint64 runningIdx = 0x10;
    std::vector<SeparatorDef> updatedSeparators;

    for (int r = 0; r < m_model->rowCount(); ++r) {
        auto* priorityItem = m_model->item(r, ModColPriority);
        auto* nameItem = m_model->item(r, ModColName);
        if (!priorityItem || !nameItem) continue;
        int kind = nameItem->data(Qt::UserRole + 50).toInt();
        if (kind == RowKindSeparator) {
            QString sepName = nameItem->data(Qt::UserRole + 102).toString();
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
        int modIdx = nameItem->data(Qt::UserRole + 101).toInt();
        if (modIdx < 0 || modIdx >= int(m_mods.size())) continue;
        auto& meta = m_mods[modIdx];
        QString newVisualIndex = formatHexIndex(runningIdx);
        QString newSeparator = currentSeparator;
        bool dirty = (meta.visualIndex != newVisualIndex) || (meta.separator != newSeparator);
        meta.visualIndex = newVisualIndex;
        meta.separator = newSeparator;
        if (dirty) {
            QString yamlPath = m_modsDir + "/" + meta.folder + "/metadata.yaml";
            patchMetadataField(yamlPath, "visual_index", newVisualIndex);
            patchMetadataField(yamlPath, "separator", newSeparator);
        }
        runningIdx += 0x10;
    }

    m_separators = updatedSeparators;
    persistSeparators();
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
            QMessageBox::warning(this, "Duplicate",
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
    int sepRow = -1;
    for (int r = 0; r < m_model->rowCount(); ++r) {
        auto* item = m_model->item(r, ModColName);
        if (item && item->data(Qt::UserRole + 50).toInt() == RowKindSeparator &&
            item->data(Qt::UserRole + 102).toString() == name) {
            sepRow = r;
            break;
        }
    }
    if (sepRow >= 0 && visualRow >= 0 && visualRow != sepRow) {
        auto items = m_model->takeRow(sepRow);
        int insertAt = visualRow;
        if (sepRow < visualRow) insertAt--;
        m_model->insertRow(insertAt, items);
    }
    persistRowOrder();
}

void ModListWidget::renameSeparator(int row)
{
    auto* item = m_model->item(row, ModColName);
    if (!item || item->data(Qt::UserRole + 50).toInt() != RowKindSeparator) return;
    QString oldName = item->data(Qt::UserRole + 102).toString();
    bool ok = false;
    QString newName = QInputDialog::getText(m_view, "Rename Separator",
        "New name:", QLineEdit::Normal, oldName, &ok);
    newName = newName.trimmed();
    if (!ok || newName.isEmpty() || newName == oldName) return;
    for (auto& meta : m_mods) {
        if (meta.separator == oldName) {
            meta.separator = newName;
            QString yamlPath = m_modsDir + "/" + meta.folder + "/metadata.yaml";
            patchMetadataField(yamlPath, "separator", newName);
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
    auto* item = m_model->item(row, ModColName);
    if (!item || item->data(Qt::UserRole + 50).toInt() != RowKindSeparator) return;
    QString name = item->data(Qt::UserRole + 102).toString();
    for (auto& meta : m_mods) {
        if (meta.separator == name) {
            meta.separator.clear();
            QString yamlPath = m_modsDir + "/" + meta.folder + "/metadata.yaml";
            patchMetadataField(yamlPath, "separator", QString());
        }
    }
    m_separators.erase(std::remove_if(m_separators.begin(), m_separators.end(),
        [&](const SeparatorDef& s) { return s.name == name; }), m_separators.end());
    persistSeparators();
    rebuildView();
}

void ModListWidget::toggleCollapseAt(int row)
{
    auto* item = m_model->item(row, ModColName);
    if (!item || item->data(Qt::UserRole + 50).toInt() != RowKindSeparator) return;
    QString name = item->data(Qt::UserRole + 102).toString();
    for (auto& s : m_separators) {
        if (s.name == name) { s.collapsed = !s.collapsed; break; }
    }
    persistSeparators();
    rebuildView();
}

void ModListWidget::moveSeparatorTo(int row, bool toTop)
{
    auto* item = m_model->item(row, ModColName);
    if (!item || item->data(Qt::UserRole + 50).toInt() != RowKindSeparator)
        return;
    QString name = item->data(Qt::UserRole + 102).toString();
    if (name.isEmpty() || m_separators.empty())
        return;

    // Bracket the new index just outside the current min/max so rebuildView
    // sorts this separator to the desired end. persistRowOrder then
    // renumbers the whole list sequentially so we don't accumulate gaps.
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

    int modelRow = -1;
    for (int r = 0; r < m_model->rowCount(); ++r) {
        auto* nameItem = m_model->item(r, ModColName);
        if (!nameItem) continue;
        if (nameItem->data(Qt::UserRole + 50).toInt() != RowKindMod) continue;
        if (nameItem->data(Qt::UserRole + 101).toInt() == modIdx) {
            modelRow = r;
            break;
        }
    }
    if (modelRow >= 0) {
        auto* catItem = m_model->item(modelRow, ModColCategory);
        if (catItem) catItem->setText(category);
    }

    QString metaPath = m_modsDir + "/" + m_mods[modIdx].folder + "/metadata.yaml";
    patchMetadataField(metaPath, "category", category);
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
    if (!m_grpc->extractOverwriteToMod(m_gameId, name.trimmed(), {}, /*keep=*/false, count, err)) {
        QMessageBox::warning(this, "Extract Failed", err);
        return;
    }
    QMessageBox::information(this, "Extract Complete",
        QString("Moved %1 file(s) into mod \"%2\".").arg(count).arg(name.trimmed()));
    scanModsFolder();
}

// Pops a multi-select picker so the user can graduate a subset of Overwrite into a named mod folder.
void ModListWidget::extractOverwriteSelected()
{
    std::vector<GrpcOverwriteEntry> files;
    QString owDir, err;
    if (!m_grpc->listOverwriteFiles(m_gameId, files, owDir, err)) {
        QMessageBox::warning(this, "Extract Failed", err);
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
        QMessageBox::information(this, "Extract", "Nothing selected.");
        return;
    }
    QString name = nameEdit->text().trimmed();
    if (name.isEmpty()) {
        QMessageBox::warning(this, "Extract", "Mod name is required.");
        return;
    }

    int count = 0;
    QString rpcErr;
    if (!m_grpc->extractOverwriteToMod(m_gameId, name, chosen, keepCb->isChecked(),
                                        count, rpcErr)) {
        QMessageBox::warning(this, "Extract Failed", rpcErr);
        return;
    }
    QMessageBox::information(this, "Extract Complete",
        QString("Moved %1 file(s) into mod \"%2\".").arg(count).arg(name));
    scanModsFolder();
}

void ModListWidget::onAddSeparatorClicked()
{
    if (m_view->isHidden())
        return;
    if (!m_visualMode) {
        // Toggling the checkbox flows through onVisualToggled →
        // rebuildView + persistSeparators, leaving the model in a
        // consistent visual-mode state before we insert.
        m_visualCheck->setChecked(true);
    }
    bool atTop = QApplication::keyboardModifiers().testFlag(Qt::ShiftModifier);
    int targetRow = atTop ? 0 : (m_model->rowCount() - 1);
    if (targetRow < 0)
        targetRow = 0;
    createSeparatorAt(targetRow);
}

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
        QMessageBox::information(this, "Group by Category",
            "No mods have a category set. Assign categories first "
            "(right-click a mod → Set Category).");
        return;
    }

    auto reply = QMessageBox::question(this, "Group by Category",
        QString("Create separators for %1 distinct categor%2 and assign every "
                "categorized mod to its matching separator?\n\n"
                "Mods currently in a different separator will be reassigned. "
                "Mods with no category are left untouched.")
            .arg(cats.size()).arg(cats.size() == 1 ? "y" : "ies"),
        QMessageBox::Yes | QMessageBox::No);
    if (reply != QMessageBox::Yes)
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
        patchMetadataField(yamlPath, "separator", c);
    }

    if (!m_visualMode) {
        m_visualMode = true;
        QSignalBlocker block(m_visualCheck);
        m_visualCheck->setChecked(true);
    }
    rebuildView();
    persistRowOrder();
}

} // namespace gorganizer
