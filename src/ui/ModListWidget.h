#pragma once

#include <QWidget>
#include <QTreeView>
#include <QStandardItemModel>
#include "GameInfo.h"
#include "GrpcClient.h"
#include <vector>

class QDropEvent;
class QCheckBox;
class QPushButton;

namespace gorganizer {

enum ModColumn { ModColPriority = 0, ModColConflicts = 1, ModColName = 2, ModColCategory = 3, ModColVersion = 4 };

// Row kinds in the model; RowKindOverwrite is the pinned-bottom write-capture pseudo-row.
enum ModRowKind { RowKindMod = 0, RowKindSeparator = 1, RowKindOverwrite = 2 };

inline constexpr const char* kOverwriteModName = "Overwrite";

// Parsed from metadata.yaml in each mod folder.
struct ModMetadata {
    QString name;
    QString folder;
    QString installed;
    QString sourceArchive;
    QStringList sourceArchives;
    QString nexusUrl;
    QString category;
    QString version;
    bool enabled = true;
    int fileCount = 0;
    QString trueIndex;
    QString visualIndex;
    QString separator;
};

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
    // Toggle the global "fuse separator+true views" setting. While on, the
    // Separator View checkbox is forced on/disabled; reorders also stamp
    // true_index to match visual_index. Off leaves any stamped indices alone.
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
    static ModMetadata readMetadata(const QString& yamlPath);
    void recalculatePriorities();
    void applySort(int column, Qt::SortOrder order);
    void restorePriorityOrder();
    void setCategoryForRow(int row, const QString& category);
    // Sets or clears the mod_page key in metadata.yaml; empty url removes the key.
    void updateModPageUrl(int row, const QString& url);

    void onVisualToggled(bool on);
    void rebuildView();
    void persistRowOrder();
    void createSeparatorAt(int visualRow);
    void renameSeparator(int row);
    void removeSeparator(int row);
    void toggleCollapseAt(int row);
    void moveSeparatorTo(int row, bool toTop);
    void persistSeparators();
    void onAddSeparatorClicked();
    void groupByCategory();
    // Sets/unsets a single top-level key in metadata.yaml without disturbing other lines.
    static void patchMetadataField(const QString& yamlPath, const QString& key,
                                    const QString& value);

    GrpcClient* m_grpc;
    ModListTreeView* m_view;
    QStandardItemModel* m_model;
    QWidget* m_placeholder;
    QCheckBox* m_visualCheck = nullptr;
    QPushButton* m_addSeparatorBtn = nullptr;

    std::vector<GrpcFileConflict> m_conflicts;
    void repaintConflictHighlights();
    void showConflictDetailsForMod(const QString& modName);

    void appendOverwriteRow();
    void onOverwriteContextMenu(const QPoint& globalPos);
    void extractOverwriteAll();
    void extractOverwriteSelected();

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

} // namespace gorganizer
