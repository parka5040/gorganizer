#pragma once

#include <QWidget>
#include <QTreeView>
#include <QStandardItemModel>
#include "GameInfo.h"

class QDropEvent;

namespace gorganizer {

struct PluginEntry;

enum PluginRole {
    PluginTypeRole   = Qt::UserRole + 1,
    PinnedRole       = Qt::UserRole + 2,
    LoadOrderRow     = Qt::UserRole + 3,
};

enum PluginColumn { ColIndex = 0, ColPlugin = 1, ColType = 2 };

class PluginListWidget;

class LoadOrderTreeView : public QTreeView {
    Q_OBJECT
public:
    explicit LoadOrderTreeView(PluginListWidget* owner, QWidget* parent = nullptr);

protected:
    void dropEvent(QDropEvent* event) override;

private:
    int dropTargetRow(QDropEvent* event) const;
    bool isMoveAllowed(int sourceRow, int destRow) const;
    PluginListWidget* m_owner;
};

class PluginListWidget : public QWidget {
    Q_OBJECT
public:
    explicit PluginListWidget(QWidget* parent = nullptr);

    void loadForGame(const GameInfo& game);
    // Set the mods directory so plugins from enabled mods are included.
    void setModsDir(const QString& modsDir);
    // Refresh the plugin list (call after a mod is enabled/disabled).
    void refresh();

private slots:
    void onHeaderClicked(int column);

private:
    void populateLoadOrder(const std::vector<PluginEntry>& plugins,
                           const QString& gameShortName);
    std::vector<PluginEntry> collectPlugins();
    static QString typeString(int type);
    static bool isGameMaster(const QString& filename, const QString& gameShortName);

    friend class LoadOrderTreeView;
    void recalculateIndices();
    void applySort(int column, Qt::SortOrder order);
    void restoreLoadOrder();

    LoadOrderTreeView* m_view;
    QStandardItemModel* m_model;
    QWidget* m_placeholder;
    GameInfo m_game;
    QString m_modsDir;

    int m_sortColumn = ColIndex;
    Qt::SortOrder m_sortOrder = Qt::AscendingOrder;
};

} // namespace gorganizer
