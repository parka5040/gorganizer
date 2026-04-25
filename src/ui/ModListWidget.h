#pragma once

#include <QWidget>
#include <QTreeView>
#include <QStandardItemModel>
#include "GameInfo.h"
#include "GrpcClient.h"
#include <vector>

class QDropEvent;
class QCheckBox;

namespace gorganizer {

enum ModColumn { ModColPriority = 0, ModColConflicts = 1, ModColName = 2, ModColCategory = 3, ModColVersion = 4 };

// Row kinds used inside the model. Stored on the priority column's
// Qt::UserRole + 50 so the view can treat separator rows specially
// (collapse, drag rules) without touching the existing data layout.
//
// RowKindOverwrite is the always-on, always-bottom pseudo-row for the
// Overwrite layer — italicized + centered, no checkbox, not dragable, not
// counted in priority numbering. The user interacts with it only via
// right-click (Open Folder, Extract All to Mod, Extract Selected...).
enum ModRowKind { RowKindMod = 0, RowKindSeparator = 1, RowKindOverwrite = 2 };

// Reserved name for the Overwrite layer; mirrors profile.OverwriteModName.
inline constexpr const char* kOverwriteModName = "Overwrite";

// Parsed from metadata.yaml in each mod folder.
struct ModMetadata {
    QString name;
    QString folder;
    QString installed;
    QString sourceArchive;            // legacy scalar (pre-merge schema)
    QStringList sourceArchives;       // ordered merge list (paths rel to ModsDir)
    QString nexusUrl;
    QString category;
    QString version;
    bool enabled = true;
    int fileCount = 0;
    // MO2-style mod-list layout state (see internal/separators on the Go
    // side). Empty strings are normal for mods the user has never touched
    // in Visual mode.
    QString trueIndex;   // 16-char hex — mirrors modlist.txt position
    QString visualIndex; // 16-char hex — independent Visual-mode position
    QString separator;   // name of the separator this mod belongs to
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

    // True when Visual mode is on (separators visible, drag reorders
    // visual_index only). False = flat list, drag reorders modlist.txt
    // (true load order) and separators are invisible.
    bool visualModeEnabled() const;

signals:
    void modToggled();  // emitted when a mod is enabled/disabled

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
    // Rewrites metadata.yaml in place, setting or clearing the `mod_page`
    // key. Empty url clears the key entirely so the context menu stops
    // showing "Visit Mod Page".
    void updateModPageUrl(int row, const QString& url);

    // --- Visual mode / separator support ---
    void onVisualToggled(bool on);
    void rebuildView();                     // reshuffles model rows per current mode
    void persistRowOrder();                 // writes new positions to disk (mode-aware)
    void createSeparatorAt(int visualRow);  // context menu "Add Separator Here"
    void renameSeparator(int row);
    void removeSeparator(int row);
    void toggleCollapseAt(int row);
    void persistSeparators();
    // Atomic helper that sets/unsets a single top-level key in a mod's
    // metadata.yaml without disturbing surrounding lines or list blocks.
    static void patchMetadataField(const QString& yamlPath, const QString& key,
                                    const QString& value);

    GrpcClient* m_grpc;
    ModListTreeView* m_view;
    QStandardItemModel* m_model;
    QWidget* m_placeholder;
    QCheckBox* m_visualCheck = nullptr;

    // Latest per-file conflict snapshot, cached so the UI can repaint
    // selection-driven highlights and pop a per-file detail dialog without
    // round-tripping the daemon on every click.
    std::vector<GrpcFileConflict> m_conflicts;
    void repaintConflictHighlights();
    void showConflictDetailsForMod(const QString& modName);

    // Overwrite-row helpers — kept separate from the per-mod menu so the
    // pinned row never gets mistaken for a regular mod (no Reinstall, no
    // Set Category, no checkbox toggle).
    void appendOverwriteRow();
    void onOverwriteContextMenu(const QPoint& globalPos);
    void extractOverwriteAll();
    void extractOverwriteSelected();

    QString m_gameId;
    QString m_profileName;
    QString m_modsDir;
    GameInfo m_activeGame;
    std::vector<ModMetadata> m_mods;
    // Separators live outside the mod folders — they're per-profile. Kept
    // as a parallel vector with the same fields the daemon persists.
    struct SeparatorDef {
        QString name;
        QString visualIndex;
        bool collapsed = false;
    };
    std::vector<SeparatorDef> m_separators;
    bool m_visualMode = false;
    bool m_updatingModel = false;

    int m_sortColumn = ModColPriority;
    Qt::SortOrder m_sortOrder = Qt::AscendingOrder;
};

} // namespace gorganizer
