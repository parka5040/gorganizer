#pragma once

#include <QWidget>
#include <QTreeView>
#include <QStandardItemModel>
#include <vector>
#include "GameInfo.h"

class QDropEvent;

namespace gorganizer {

struct PluginEntry;
struct GrpcPluginStatus;
class GrpcClient;

enum PluginRole {
    PluginTypeRole   = Qt::UserRole + 1,
    PinnedRole       = Qt::UserRole + 2,
    LoadOrderRow     = Qt::UserRole + 3,
    DepIssuesRole    = Qt::UserRole + 4, // QStringList of human-readable issues
    DepWorstKindRole = Qt::UserRole + 5, // int — highest GrpcDepKind on the row
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

    // Wire the gRPC client whose StreamPluginStatus this widget consumes.
    // Pass nullptr to detach (e.g. on shutdown).
    void setGrpcClient(GrpcClient* grpc);
    // Set the active profile name. Together with the active game, this
    // is the (gameID, profile) tuple StreamPluginStatus subscribes on.
    // Empty string unsubscribes.
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

    friend class LoadOrderTreeView;
    void recalculateIndices();
    void applySort(int column, Qt::SortOrder order);
    void restoreLoadOrder();

    LoadOrderTreeView* m_view;
    QStandardItemModel* m_model;
    QWidget* m_placeholder;
    GameInfo m_game;
    QString m_modsDir;
    QString m_activeProfile;
    GrpcClient* m_grpc = nullptr;

    int m_sortColumn = ColIndex;
    Qt::SortOrder m_sortOrder = Qt::AscendingOrder;
};

} // namespace gorganizer
