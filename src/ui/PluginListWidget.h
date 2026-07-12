#pragma once

#include <QWidget>
#include <QTreeView>
#include <vector>
#include "GameInfo.h"
#include "GrpcClient.h"
#include "PluginListModel.h"

class QDropEvent;

namespace gorganizer {

class GrpcClient;
class PluginListWidget;

class LoadOrderTreeView : public QTreeView {
    Q_OBJECT
public:
    explicit LoadOrderTreeView(PluginListWidget* owner, QWidget* parent = nullptr);

protected:
    void dropEvent(QDropEvent* event) override;

private:
    int dropTargetRow(QDropEvent* event) const;
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

private:
    friend class LoadOrderTreeView;
    void resubscribeStream();
    // Persists the ordered activation state and reloads on failure.
    void persistLoadoutToDaemon();

    LoadOrderTreeView* m_view;
    PluginListModel* m_model;
    QWidget* m_placeholder;
    GameInfo m_game;
    QString m_activeProfile;
    GrpcClient* m_grpc = nullptr;

    int m_sortColumn = PluginColIndex;
    Qt::SortOrder m_sortOrder = Qt::AscendingOrder;
};

}
