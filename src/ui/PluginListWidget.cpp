#include "PluginListWidget.h"
#include "PluginScanner.h"

#include <QVBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QDropEvent>
#include <QDir>
#include <QFile>
#include <QSet>
#include <algorithm>

namespace gorganizer {

// --- LoadOrderTreeView ---

LoadOrderTreeView::LoadOrderTreeView(PluginListWidget* owner, QWidget* parent)
    : QTreeView(parent)
    , m_owner(owner)
{
}

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

bool LoadOrderTreeView::isMoveAllowed(int sourceRow, int destRow) const
{
    auto* m = static_cast<QStandardItemModel*>(model());
    int count = m->rowCount();

    if (sourceRow < 0 || sourceRow >= count)
        return false;
    if (destRow < 0 || destRow > count)
        return false;
    if (sourceRow == destRow || sourceRow + 1 == destRow)
        return false;

    auto* srcPlugin = m->item(sourceRow, ColPlugin);
    if (!srcPlugin)
        return false;

    if (srcPlugin->data(PinnedRole).toBool())
        return false;

    int srcType = srcPlugin->data(PluginTypeRole).toInt();

    int insertAt = destRow;
    if (sourceRow < destRow)
        insertAt--;

    auto typeAtEffective = [&](int effectiveRow) -> int {
        int modelRow = effectiveRow;
        if (modelRow >= sourceRow)
            modelRow++;
        if (modelRow < 0 || modelRow >= count)
            return -1;
        auto* ti = m->item(modelRow, ColPlugin);
        return ti ? ti->data(PluginTypeRole).toInt() : -1;
    };

    auto pinnedAtEffective = [&](int effectiveRow) -> bool {
        int modelRow = effectiveRow;
        if (modelRow >= sourceRow)
            modelRow++;
        if (modelRow < 0 || modelRow >= count)
            return false;
        auto* pi = m->item(modelRow, ColPlugin);
        return pi ? pi->data(PinnedRole).toBool() : false;
    };

    if (insertAt < count - 1 && pinnedAtEffective(insertAt))
        return false;

    if (insertAt > 0) {
        int aboveType = typeAtEffective(insertAt - 1);
        if (aboveType > srcType)
            return false;
    }

    if (insertAt < count - 1) {
        int belowType = typeAtEffective(insertAt);
        if (belowType >= 0 && belowType < srcType)
            return false;
    }

    return true;
}

void LoadOrderTreeView::dropEvent(QDropEvent* event)
{
    if (!model())
        return;

    // Reject drops while a non-load-order sort is active.
    if (!(m_owner->m_sortColumn == ColIndex && m_owner->m_sortOrder == Qt::AscendingOrder)) {
        event->ignore();
        return;
    }

    auto selected = selectionModel()->selectedRows(ColPlugin);
    if (selected.isEmpty()) {
        event->ignore();
        return;
    }

    int sourceRow = selected.first().row();
    int destRow = dropTargetRow(event);

    if (!isMoveAllowed(sourceRow, destRow)) {
        event->ignore();
        return;
    }

    auto* m = static_cast<QStandardItemModel*>(model());

    auto items = m->takeRow(sourceRow);
    int insertAt = destRow;
    if (sourceRow < destRow)
        insertAt--;
    m->insertRow(insertAt, items);

    // Update stored load-order positions.
    for (int i = 0; i < m->rowCount(); ++i) {
        auto* pi = m->item(i, ColPlugin);
        if (pi)
            pi->setData(i, LoadOrderRow);
    }

    m_owner->recalculateIndices();

    selectionModel()->select(m->index(insertAt, 0),
                             QItemSelectionModel::ClearAndSelect | QItemSelectionModel::Rows);

    event->setDropAction(Qt::CopyAction);
    event->accept();
}

// --- PluginListWidget ---

PluginListWidget::PluginListWidget(QWidget* parent)
    : QWidget(parent)
{
    auto* layout = new QVBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);

    auto* titleLabel = new QLabel("Plugins");
    titleLabel->setStyleSheet("font-weight: bold;");
    layout->addWidget(titleLabel);

    m_model = new QStandardItemModel(this);
    m_model->setHorizontalHeaderLabels({"Index", "Plugin", "Type"});

    m_view = new LoadOrderTreeView(this);
    m_view->setModel(m_model);
    m_view->setRootIsDecorated(false);
    m_view->setSelectionMode(QAbstractItemView::SingleSelection);
    m_view->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_view->setDragEnabled(true);
    m_view->setAcceptDrops(true);
    m_view->setDropIndicatorShown(true);
    m_view->setDragDropMode(QAbstractItemView::DragDrop);
    m_view->setDefaultDropAction(Qt::MoveAction);

    // Clickable headers for sorting. We handle sorting ourselves.
    m_view->setSortingEnabled(false);
    m_view->header()->setSectionsClickable(true);
    m_view->header()->setSortIndicatorShown(true);
    m_view->header()->setSortIndicator(ColIndex, Qt::AscendingOrder);

    m_view->header()->setSectionResizeMode(ColIndex, QHeaderView::ResizeToContents);
    m_view->header()->setSectionResizeMode(ColPlugin, QHeaderView::Stretch);
    m_view->header()->setSectionResizeMode(ColType, QHeaderView::ResizeToContents);

    connect(m_view->header(), &QHeaderView::sectionClicked,
            this, &PluginListWidget::onHeaderClicked);

    layout->addWidget(m_view);

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

void PluginListWidget::onHeaderClicked(int column)
{
    if (m_sortColumn == column) {
        if (m_sortOrder == Qt::AscendingOrder) {
            m_sortOrder = Qt::DescendingOrder;
        } else {
            // Third click: reset to Index ascending (the natural load order).
            m_sortColumn = ColIndex;
            m_sortOrder = Qt::AscendingOrder;
            m_view->header()->setSortIndicator(ColIndex, Qt::AscendingOrder);
            restoreLoadOrder();
            m_view->setDragEnabled(true);
            return;
        }
    } else {
        m_sortColumn = column;
        m_sortOrder = Qt::AscendingOrder;
    }

    m_view->header()->setSortIndicator(m_sortColumn, m_sortOrder);

    // Index ascending IS the load order — just restore, don't disable drag.
    if (m_sortColumn == ColIndex && m_sortOrder == Qt::AscendingOrder) {
        restoreLoadOrder();
        m_view->setDragEnabled(true);
    } else {
        m_view->setDragEnabled(false);
        applySort(m_sortColumn, m_sortOrder);
    }
}

void PluginListWidget::applySort(int column, Qt::SortOrder order)
{
    struct RowData {
        QList<QStandardItem*> items;
        int loadOrder;
        QString text;
    };

    QVector<RowData> rows;
    rows.reserve(m_model->rowCount());
    while (m_model->rowCount() > 0) {
        auto items = m_model->takeRow(0);
        int lo = items[ColPlugin]->data(LoadOrderRow).toInt();
        QString sortKey = items[column]->text();
        rows.append({items, lo, sortKey});
    }

    if (column == ColIndex) {
        // Sort by load-order position numerically.
        std::sort(rows.begin(), rows.end(), [order](const RowData& a, const RowData& b) {
            if (order == Qt::AscendingOrder)
                return a.loadOrder < b.loadOrder;
            return a.loadOrder > b.loadOrder;
        });
    } else {
        // Sort by text (alphabetical).
        std::sort(rows.begin(), rows.end(), [order](const RowData& a, const RowData& b) {
            int cmp = a.text.compare(b.text, Qt::CaseInsensitive);
            return order == Qt::AscendingOrder ? cmp < 0 : cmp > 0;
        });
    }

    for (auto& rd : rows)
        m_model->appendRow(rd.items);
}

void PluginListWidget::restoreLoadOrder()
{
    struct RowData {
        QList<QStandardItem*> items;
        int loadOrder;
    };

    QVector<RowData> rows;
    rows.reserve(m_model->rowCount());
    while (m_model->rowCount() > 0) {
        auto items = m_model->takeRow(0);
        int lo = items[ColPlugin]->data(LoadOrderRow).toInt();
        rows.append({items, lo});
    }

    std::sort(rows.begin(), rows.end(), [](const RowData& a, const RowData& b) {
        return a.loadOrder < b.loadOrder;
    });

    for (auto& rd : rows)
        m_model->appendRow(rd.items);
}

void PluginListWidget::setModsDir(const QString& modsDir)
{
    m_modsDir = modsDir;
}

void PluginListWidget::loadForGame(const GameInfo& game)
{
    m_game = game;
    m_model->removeRows(0, m_model->rowCount());
    m_sortColumn = ColIndex;
    m_sortOrder = Qt::AscendingOrder;
    m_view->header()->setSortIndicator(ColIndex, Qt::AscendingOrder);

    if (!game.detected || game.dataDir.empty()) {
        m_view->hide();
        m_placeholder->show();
        return;
    }

    auto plugins = collectPlugins();
    if (plugins.empty()) {
        m_view->hide();
        m_placeholder->show();
        return;
    }

    populateLoadOrder(plugins, game.shortName);

    m_placeholder->hide();
    m_view->show();
}

void PluginListWidget::refresh()
{
    if (m_game.detected)
        loadForGame(m_game);
}

std::vector<PluginEntry> PluginListWidget::collectPlugins()
{
    // Scan the base game Data/ directory.
    auto plugins = PluginScanner::scan(m_game.dataDir);

    // Also scan enabled mod folders for additional plugins.
    if (!m_modsDir.isEmpty()) {
        QDir modsDir(m_modsDir);
        if (modsDir.exists()) {
            // Track which plugin filenames we already have (case-insensitive).
            QSet<QString> seen;
            for (const auto& p : plugins)
                seen.insert(p.filename.toLower());

            auto modDirs = modsDir.entryList(QDir::Dirs | QDir::NoDotAndDotDot);
            for (const auto& modDirName : modDirs) {
                // Check if this mod is enabled via metadata.yaml.
                QString metaPath = m_modsDir + "/" + modDirName + "/metadata.yaml";
                bool enabled = false;
                QFile metaFile(metaPath);
                if (metaFile.open(QIODevice::ReadOnly | QIODevice::Text)) {
                    while (!metaFile.atEnd()) {
                        QString line = metaFile.readLine().trimmed();
                        if (line.startsWith("enabled:")) {
                            enabled = line.contains("true");
                            break;
                        }
                    }
                    metaFile.close();
                }
                if (!enabled)
                    continue;

                // Scan this mod folder for plugins.
                QString modPath = m_modsDir + "/" + modDirName;
                auto modPlugins = PluginScanner::scan(std::filesystem::path(modPath.toStdString()));
                for (const auto& p : modPlugins) {
                    if (!seen.contains(p.filename.toLower())) {
                        plugins.push_back(p);
                        seen.insert(p.filename.toLower());
                    }
                }
            }

            // Re-sort: ESMs first, then ESLs, then ESPs, alphabetical within each group.
            std::sort(plugins.begin(), plugins.end(),
                      [](const PluginEntry& a, const PluginEntry& b) {
                          if (a.type != b.type)
                              return a.type < b.type;
                          return a.filename.compare(b.filename, Qt::CaseInsensitive) < 0;
                      });
        }
    }
    return plugins;
}

bool PluginListWidget::isGameMaster(const QString& filename, const QString& gameShortName)
{
    // TODO: When TTW (Tale of Two Wastelands) flag is implemented, allow
    // reordering FalloutNV.esm/Fallout3.esm relative to each other.
    static const QHash<QString, QStringList> gameMasters = {
        {"morrowind",  {"Morrowind.esm", "Tribunal.esm", "Bloodmoon.esm"}},
        {"oblivion",   {"Oblivion.esm"}},
        {"skyrim",     {"Skyrim.esm"}},
        {"skyrimse",   {"Skyrim.esm", "Update.esm", "Dawnguard.esm", "HearthFires.esm", "Dragonborn.esm"}},
        {"fallout3",   {"Fallout3.esm"}},
        {"falloutnv",  {"FalloutNV.esm"}},
        {"fallout4",   {"Fallout4.esm"}},
        {"starfield",  {"Starfield.esm"}},
    };

    auto it = gameMasters.find(gameShortName);
    if (it == gameMasters.end())
        return false;

    for (const auto& master : it.value()) {
        if (filename.compare(master, Qt::CaseInsensitive) == 0)
            return true;
    }
    return false;
}

void PluginListWidget::populateLoadOrder(const std::vector<PluginEntry>& plugins,
                                          const QString& gameShortName)
{
    static const QHash<QString, QStringList> gameMasters = {
        {"morrowind",  {"Morrowind.esm", "Tribunal.esm", "Bloodmoon.esm"}},
        {"oblivion",   {"Oblivion.esm"}},
        {"skyrim",     {"Skyrim.esm"}},
        {"skyrimse",   {"Skyrim.esm", "Update.esm", "Dawnguard.esm", "HearthFires.esm", "Dragonborn.esm"}},
        {"fallout3",   {"Fallout3.esm"}},
        {"falloutnv",  {"FalloutNV.esm"}},
        {"fallout4",   {"Fallout4.esm"}},
        {"starfield",  {"Starfield.esm"}},
    };
    QStringList masterOrder;
    if (gameMasters.contains(gameShortName))
        masterOrder = gameMasters[gameShortName];

    std::vector<const PluginEntry*> pinned;
    std::vector<const PluginEntry*> rest;

    for (const auto& p : plugins) {
        if (isGameMaster(p.filename, gameShortName))
            pinned.push_back(&p);
        else
            rest.push_back(&p);
    }

    std::sort(pinned.begin(), pinned.end(), [&](const PluginEntry* a, const PluginEntry* b) {
        int ia = masterOrder.indexOf(a->filename);
        int ib = masterOrder.indexOf(b->filename);
        if (ia < 0) ia = 9999;
        if (ib < 0) ib = 9999;
        return ia < ib;
    });

    int rowIndex = 0;
    auto addRow = [&](const PluginEntry& p, bool isPinned) {
        auto* indexItem = new QStandardItem;
        indexItem->setEditable(false);
        indexItem->setTextAlignment(Qt::AlignCenter);
        indexItem->setDragEnabled(false);

        auto* nameItem = new QStandardItem(p.filename);
        nameItem->setCheckable(true);
        nameItem->setCheckState(Qt::Checked);
        nameItem->setEditable(false);
        nameItem->setData(static_cast<int>(p.type), PluginTypeRole);
        nameItem->setData(isPinned, PinnedRole);
        nameItem->setData(rowIndex, LoadOrderRow);
        nameItem->setDragEnabled(!isPinned);

        if (isPinned) {
            nameItem->setEnabled(false);
            nameItem->setCheckState(Qt::Checked);
            auto font = nameItem->font();
            font.setItalic(true);
            nameItem->setFont(font);
        }

        auto* typeItem = new QStandardItem(typeString(p.type));
        typeItem->setEditable(false);
        typeItem->setData(static_cast<int>(p.type), PluginTypeRole);
        typeItem->setTextAlignment(Qt::AlignCenter);
        typeItem->setDragEnabled(!isPinned);

        QColor color;
        switch (p.type) {
        case PluginEntry::ESM: color = QColor(100, 149, 237); break;
        case PluginEntry::ESL: color = QColor(144, 238, 144); break;
        case PluginEntry::ESP: color = QColor(210, 210, 210); break;
        }
        nameItem->setForeground(color);
        typeItem->setForeground(color);

        m_model->appendRow({indexItem, nameItem, typeItem});
        rowIndex++;
    };

    for (const auto* p : pinned)
        addRow(*p, true);
    for (const auto* p : rest)
        addRow(*p, false);

    recalculateIndices();
}

void PluginListWidget::recalculateIndices()
{
    struct Entry {
        int visualRow;
        int loadOrder;
        int type;
    };

    QVector<Entry> entries;
    entries.reserve(m_model->rowCount());
    for (int i = 0; i < m_model->rowCount(); ++i) {
        auto* pi = m_model->item(i, ColPlugin);
        if (!pi) continue;
        entries.append({i, pi->data(LoadOrderRow).toInt(), pi->data(PluginTypeRole).toInt()});
    }

    std::sort(entries.begin(), entries.end(), [](const Entry& a, const Entry& b) {
        return a.loadOrder < b.loadOrder;
    });

    int fullIndex = 0;
    int lightIndex = 0;
    for (const auto& e : entries) {
        auto* indexItem = m_model->item(e.visualRow, ColIndex);
        if (!indexItem) continue;

        if (e.type == PluginEntry::ESL) {
            indexItem->setText(QString("FE:%1").arg(lightIndex, 3, 16, QChar('0')).toUpper());
            lightIndex++;
        } else {
            indexItem->setText(QString("%1").arg(fullIndex, 2, 16, QChar('0')).toUpper());
            fullIndex++;
        }
    }
}

QString PluginListWidget::typeString(int type)
{
    switch (type) {
    case PluginEntry::ESM: return "ESM";
    case PluginEntry::ESL: return "ESL";
    case PluginEntry::ESP: return "ESP";
    default: return "???";
    }
}

} // namespace gorganizer
