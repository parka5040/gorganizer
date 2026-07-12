#pragma once

#include <QWidget>
#include <QTreeView>
#include "GameInfo.h"
#include "GrpcClient.h"
#include "ModCatalog.h"
#include "ModListModel.h"
#include <vector>

class QDropEvent;
class QCheckBox;
class QPushButton;

namespace gorganizer {

class ModListWidget;

class ModListTreeView : public QTreeView {
    Q_OBJECT
public:
    explicit ModListTreeView(ModListWidget* owner, QWidget* parent = nullptr);

protected:
    void dropEvent(QDropEvent* event) override;

private:
    int dropTargetRow(QDropEvent* event) const;
    ModListWidget* m_owner;
};

class ModListWidget : public QWidget {
    Q_OBJECT
public:
    explicit ModListWidget(GrpcClient* grpc, QWidget* parent = nullptr);

    void loadForGame(const GameInfo& game);
    void loadForGame(const GameInfo& game, const QString& profileName);

    static QStringList defaultCategories();

    bool visualModeEnabled() const;

public slots:
    // Toggles the global fused separator+true view; while on, reorders also stamp true_index to match visual_index.
    void applyCollapsedSeparatorView(bool on);

signals:
    void modToggled();

private slots:
    void onConflictsReceived(const std::vector<GrpcFileConflict>& conflicts);
    void onModelDataChanged(const QModelIndex& topLeft, const QModelIndex& bottomRight,
                            const QList<int>& roles);
    void onHeaderClicked(int column);
    void onItemDoubleClicked(const QModelIndex& index);
    void onContextMenu(const QPoint& pos);
    void onSelectionChanged();

private:
    friend class ModListTreeView;
    void scanModsFolder();
    void restorePriorityOrder();
    void setCategoryForRow(int modIdx, const QString& category);
    // Sets or clears the mod_page key in metadata.yaml; empty url removes the key.
    void updateModPageUrl(int row, const QString& url);

    void onVisualToggled(bool on);
    void rebuildView();
    void applyOverwriteSpan();
    void persistRowOrder();
    void createSeparatorAt(int visualRow);
    void renameSeparator(int row);
    void removeSeparator(int row);
    void toggleCollapseAt(int row);
    void moveSeparatorTo(int row, bool toTop);
    void persistSeparators();
    void onAddSeparatorClicked();
    void groupByCategory();

    void updateConflictTints();
    void showConflictDetailsForMod(const QString& modName);

    void onOverwriteContextMenu(const QPoint& globalPos);
    void extractOverwriteAll();
    void extractOverwriteSelected();

    GrpcClient* m_grpc;
    ModListTreeView* m_view;
    ModListModel* m_model;
    QWidget* m_placeholder;
    QCheckBox* m_visualCheck = nullptr;
    QPushButton* m_addSeparatorBtn = nullptr;

    std::vector<GrpcFileConflict> m_conflicts;

    QString m_gameId;
    QString m_profileName;
    QString m_modsDir;
    GameInfo m_activeGame;
    std::vector<ModMetadata> m_mods;
    struct SeparatorDef {
        QString name;
        QString visualIndex;
        bool collapsed = false;
    };
    std::vector<SeparatorDef> m_separators;
    bool m_visualMode = false;
    bool m_collapsedSeparatorView = false;
    bool m_updatingModel = false;

    int m_sortColumn = ModColPriority;
    Qt::SortOrder m_sortOrder = Qt::AscendingOrder;
};

}
