#pragma once

#include <QHash>
#include <QWidget>
#include <QTreeView>
#include "GameInfo.h"
#include "GrpcClient.h"
#include "ModCatalog.h"
#include "ModListModel.h"
#include <optional>
#include <vector>

class QDropEvent;
class QCheckBox;
class QLabel;
class QMenu;
class QPushButton;
class QTimer;

namespace gorganizer {

class ModListWidget;
class ModListSaveQueue;

class ModListTreeView : public QTreeView {
    Q_OBJECT
public:
    explicit ModListTreeView(ModListWidget* owner, QWidget* parent = nullptr);

protected:
    void dropEvent(QDropEvent* event) override;
    // Runs the drag loop as a list interaction so outside reloads wait until the drop finished.
    void startDrag(Qt::DropActions supportedActions) override;

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

    // Returns the game id whose mod list is loaded.
    QString loadedGameId() const { return m_gameId; }
    // Returns the profile whose mod list is loaded.
    QString loadedProfileName() const { return m_profileName; }
    // Reports whether a context menu or dialog opened from the list is running; outside reloads wait until it closes.
    bool isInteracting() const { return m_interactionDepth > 0; }
    // Returns a counter that grows whenever the list sends a mod-list change or reloads from disk.
    quint64 editSerial() const { return m_editSerial; }
    // Reports whether a mod folder is part of the loaded list.
    bool containsMod(const QString& folder) const;
    // Rescans the mod folders without re-reading separators, deferring until the current interaction ends.
    void reloadMods();
    // Reloads the list after a failed mod-list save and re-adopts the profile's mod list.
    void reloadAfterFailedSave(const GameInfo& game, const QString& profileName);
    // Rescans the mod folders and adopts the profile's modlist, showing every mod the list lacks as disabled; false when the list is busy or not loaded.
    bool adoptModList(const std::vector<GrpcModListEntry>& entries);
    // Enables names from the authoritative modlist and returns the tracked save request ID when flags change.
    quint64 enableModsInProfile(const std::vector<GrpcModListEntry>& authoritative, const QStringList& names,
                                QStringList* changedOut);
    bool readyForDependencyEnable() const;
    // Shows a SMAPI dependency report of the loaded profile as row indicators and context-menu actions.
    void setDependencyReport(const GrpcModDependencyReport& report);
    // Removes every SMAPI dependency indicator and forgets the report.
    void clearDependencyReport();

public slots:
    // Toggles the global fused separator+true view; while on, reorders also stamp true_index to match visual_index.
    void applyCollapsedSeparatorView(bool on);

signals:
    void modToggled();
    // The user renamed, uninstalled or reinstalled mods from the list.
    void modsEdited();
    // The last context menu or dialog opened from the list closed.
    void interactionFinished();
    // The user asked to fetch the missing SMAPI dependencies of one mod.
    void dependencyFetchRequested(const QStringList& uniqueIds);
    // The user asked to enable the disabled mods that satisfy one mod's SMAPI dependencies.
    void dependencyEnableRequested(const QStringList& modNames);
    void modListReadyForEnable();

private slots:
    void onConflictsReceived(const std::vector<GrpcFileConflict>& conflicts);
    void onModelDataChanged(const QModelIndex& topLeft, const QModelIndex& bottomRight,
                            const QList<int>& roles);
    void onHeaderClicked(int column);
    void onItemDoubleClicked(const QModelIndex& index);
    void onContextMenu(const QPoint& pos);
    void onSelectionChanged();
    // Adopts the answer to the list's own modlist request once no menu or dialog is open.
    void onProfileModListReceived(quint64 requestId, const QString& gameId, const QString& profileName,
                                  const std::vector<GrpcModListEntry>& entries);
    // Retries a failed modlist request with backoff and keeps edits locked after the last attempt.
    void onProfileModListFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                                const QString& error);
    // Sends the next modlist request after a failed one.
    void onProfileRetryTimeout();
    // Clears the tracking state of a saved dependency enable.
    void onModListSaved(quint64 requestId);
    // Reloads the saved profile after a queued mod-list save fails.
    void onModListSaveFailed(quint64 requestId);
    // Resumes a deferred profile read once every queued save has completed.
    void onModListSavesDrained();

private:
    friend class ModListTreeView;
    void scanModsFolder();
    struct ActionContext {
        QString gameId;
        QString profileName;
        QString modsDir;
    };
    ActionContext actionContext() const;
    bool matchesContext(const ActionContext& context) const;
    int modIndexForFolder(const QString& folder) const;
    int separatorIndexForName(const QString& name) const;
    int availableModIndex(const ActionContext& context, const QString& folder);
    int availableSeparatorIndex(const ActionContext& context, const QString& name);
    QString metadataPathForFolder(const QString& folder) const;
    // Builds the SetModList entries of a checkbox toggle in the profile's load order.
    std::vector<GrpcModListEntry> toggleEntries() const;
    // Rescans the mod catalog without re-reading separators.
    void rescanCatalog();
    // Scans the mod folders, keeping the loaded profile's enabled flags and order once its modlist was adopted.
    std::vector<ModMetadata> scanCatalog() const;
    // Records the order of a modlist the list sends as the loaded profile's order once its modlist was adopted.
    void noteSentModList(const std::vector<GrpcModListEntry>& entries);
    // Records an optimistic edit and queues its full mod list for saving.
    quint64 submitModList(const std::vector<GrpcModListEntry>& entries);
    // Requests the loaded profile's modlist and restarts the retry budget.
    void requestProfileModList();
    // Sends one modlist request for the loaded profile.
    void sendProfileModListRequest();
    // Forgets the adopted profile modlist, its enabled flags and its order.
    void dropProfileAdoption();
    // Reports whether the loaded profile's modlist has not been adopted.
    bool editsBlocked() const;
    // Locks or unlocks checkboxes, dragging and separator edits and shows the profile loading state.
    void updateEditLock();
    // Sets the in-memory and shown enabled flag of the named mod folders without persisting anything.
    void applyEnabledFlags(const QStringList& folders, bool enabled);
    void setSelectedModsEnabled(const ActionContext& context, const QStringList& folders, bool enabled);
    // Rebuilds the rows from m_mods in the current sort, keeping cached conflict counts, and re-requests conflicts.
    void refreshView();
    void beginInteraction();
    // Ends one interaction level, running a deferred reload and announcing the end once none remain.
    void endInteraction();
    void showContextMenu(const QPoint& pos);
    // Adds the SMAPI dependency actions for one mod folder to its context menu.
    void addDependencyActions(QMenu& menu, const QString& folder);
    void restorePriorityOrder();
    void setCategoryForFolder(const ActionContext& context, const QString& folder, const QString& category);
    void updateModPageUrl(const ActionContext& context, const QString& folder, const QString& url);

    void onVisualToggled(bool on);
    void rebuildView();
    void applyOverwriteSpan();
    QHash<QString, std::vector<int>> hiddenModsBySeparator() const;
    void persistRowOrder();
    void createSeparatorAt(const ActionContext& context, ModRowKind anchorKind, const QString& anchorName);
    void renameSeparator(const ActionContext& context, const QString& name);
    void removeSeparator(const ActionContext& context, const QString& name);
    void toggleCollapseAt(const ActionContext& context, const QString& name);
    void moveSeparatorTo(const ActionContext& context, const QString& name, bool toTop);
    void persistSeparators();
    void onAddSeparatorClicked();
    void groupByCategory();

    void updateConflictTints();
    void showConflictDetailsForMod(const QString& modName);

    void onOverwriteContextMenu(const ActionContext& context, const QPoint& globalPos);
    void extractOverwriteAll(const ActionContext& context);
    void extractOverwriteSelected(const ActionContext& context);

    GrpcClient* m_grpc;
    ModListSaveQueue* m_saveQueue;
    ModListTreeView* m_view;
    ModListModel* m_model;
    QWidget* m_placeholder;
    QCheckBox* m_visualCheck = nullptr;
    QPushButton* m_addSeparatorBtn = nullptr;
    QLabel* m_profileStateLabel = nullptr;
    QPushButton* m_profileRetryButton = nullptr;
    QTimer* m_profileRetryTimer = nullptr;

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
    int m_interactionDepth = 0;
    quint64 m_editSerial = 0;
    bool m_reloadPending = false;
    std::optional<GrpcModDependencyReport> m_dependencyReport;
    bool m_profileAdopted = false;
    QHash<QString, bool> m_profileFlags;
    QHash<QString, quint64> m_profileOrder;
    quint64 m_profileListRequestId = 0;
    quint64 m_profileListSerial = 0;
    bool m_profileListPending = false;
    int m_profileRetryAttempts = 0;
    bool m_profileLoadFailed = false;
    QString m_profileLoadError;
    bool m_restoringSavedProfile = false;
    bool m_savedProfileRestored = false;
    struct EnableSave {
        QString gameId;
        QString profileName;
        QString modsDir;
        QStringList folders;
        quint64 editSerial = 0;
    };
    QHash<quint64, EnableSave> m_enableSaves;

    int m_sortColumn = ModColPriority;
    Qt::SortOrder m_sortOrder = Qt::AscendingOrder;
};

}
