#pragma once

#include <QWidget>
#include <QTreeView>
#include <QStandardItemModel>
#include <QHash>
#include <vector>
#include "GameInfo.h"
#include "GrpcClient.h"

class QDropEvent;

namespace gorganizer {

struct PluginEntry;
class GrpcClient;

enum PluginRole {
    PluginTypeRole   = Qt::UserRole + 1,
    PinnedRole       = Qt::UserRole + 2,
    LoadOrderRow     = Qt::UserRole + 3,
    DepIssuesRole    = Qt::UserRole + 4,
    DepWorstKindRole = Qt::UserRole + 5,
};

enum PluginColumn { ColIndex = 0, ColPlugin = 1, ColType = 2, ColStatus = 3 };

class PluginListWidget;

class LoadOrderTreeView : public QTreeView {
    Q_OBJECT
public:
    explicit LoadOrderTreeView(PluginListWidget* owner, QWidget* parent = nullptr);

protected:
    void dropEvent(QDropEvent* event) override;

private:
    int dropTargetRow(QDropEvent* event) const;
    bool isMoveAllowed(int sourceRow, int destRow, QString* reason = nullptr) const;
    PluginListWidget* m_owner;
};

class PluginListWidget : public QWidget {
    Q_OBJECT
public:
    explicit PluginListWidget(QWidget* parent = nullptr);

    void loadForGame(const GameInfo& game);
    void setModsDir(const QString& modsDir);
    void refresh();

    void setGrpcClient(GrpcClient* grpc);
    void setActiveProfile(const QString& profileName);

private slots:
    void onHeaderClicked(int column);
    void onPluginStatusSnapshot(const std::vector<GrpcPluginStatus>& plugins);
    void onPluginStatusUpdate(const GrpcPluginStatus& plugin);

private:
    void populateLoadOrder(const std::vector<PluginEntry>& plugins,
                           const QString& gameShortName);
    std::vector<PluginEntry> collectPlugins();
    static QString typeString(int type);
    static bool isGameMaster(const QString& filename, const QString& gameShortName);
    void resubscribeStream();
    void applyStatusToRow(int row, const GrpcPluginStatus& s);
    // Re-apply plugin-type foregrounds + dep-status icons/tints from cached
    // status using the active theme's tokens. Called on theme change.
    void restylePluginModel();

    friend class LoadOrderTreeView;
    void recalculateIndices();
    void applySort(int column, Qt::SortOrder order);
    void restoreLoadOrder();
    // Re-evaluate cached MasterOutOfOrder issues against the current row
    // positions and re-render. Called after a drag-drop reorder so warnings
    // resolve in real time when the user manually fixes the order.
    void revalidateOrderingIssues();
    // Push the current model order to the daemon as the user-set plugin
    // order. Called after a drop so plugins.txt + dep analysis pick up
    // the change.
    void persistOrderToDaemon();

    LoadOrderTreeView* m_view;
    QStandardItemModel* m_model;
    QWidget* m_placeholder;
    GameInfo m_game;
    QString m_modsDir;
    QString m_activeProfile;
    GrpcClient* m_grpc = nullptr;

    int m_sortColumn = ColIndex;
    Qt::SortOrder m_sortOrder = Qt::AscendingOrder;

    // Latest known dep-status per plugin (lowercased filename → status).
    // Used by revalidateOrderingIssues so a drag-drop can clear stale
    // MasterOutOfOrder warnings without round-tripping the daemon.
    QHash<QString, GrpcPluginStatus> m_lastStatus;
};

} // namespace gorganizer
