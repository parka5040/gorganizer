#pragma once

#include <QDialog>
#include <QString>
#include <QList>
#include <QHash>
#include "GrpcClient.h"

class QTabWidget;
class QPlainTextEdit;
class QCheckBox;
class QLabel;
class QPushButton;
class QLineEdit;
class QSpinBox;
class QComboBox;
class QWidget;
class QShortcut;
class QVBoxLayout;

namespace gorganizer {

// IniEditorDialog is a tabbed text editor for a profile's managed INI
// files. Each tab corresponds to one Bethesda INI (e.g. Fallout.ini,
// FalloutCustom.ini, FalloutPrefs.ini). A "Custom INI active" toggle
// controls whether the profile's copies get pushed into the game's
// Documents/My Games directory at launch. When the toggle is off, edits
// are stored but have no effect on the game.
class IniEditorDialog : public QDialog {
    Q_OBJECT
public:
    IniEditorDialog(GrpcClient* grpc,
                    const QString& gameId,
                    const QString& gameDisplayName,
                    const QString& profileName,
                    QWidget* parent = nullptr);

private slots:
    void onSave();
    void onToggleEnabled(bool checked);
    void onApplyNow();
    void onTabChanged(int index);
    void onTweakToggled(const QString& tweakId, bool enabled);
    void onApplyResolution();
    void onFindShortcut();
    void onFindNext();
    void onFindClose();

private:
    struct TabHandle {
        QString filename;
        QString diskPath;
        QString originalContent;
        QPlainTextEdit* editor;
    };

    void reload();
    void markDirty(int tabIndex, bool dirty);
    bool anyDirty() const;

    GrpcClient* m_grpc;
    QString m_gameId;
    QString m_profileName;

    QTabWidget* m_tabs;
    QCheckBox* m_enabledCheck;
    QLabel* m_pathLabel;
    QLabel* m_statusLabel;
    QPushButton* m_saveBtn;

    QList<TabHandle> m_handles;
    bool m_suppressEnabledSignal = false;
    QWidget* m_tweaksTab = nullptr;
    int m_tweaksTabIndex = -1;
    QWidget* m_resolutionTab = nullptr;
    int m_resolutionTabIndex = -1;
    QComboBox* m_resolutionPreset = nullptr;
    QSpinBox* m_resolutionWidth = nullptr;
    QSpinBox* m_resolutionHeight = nullptr;
    QComboBox* m_resolutionTarget = nullptr; // which INI file to write to
    QLabel* m_resolutionStatus = nullptr;

    // Find bar (Ctrl+F) — pinned below the tabs, searches the currently
    // active editor. Stays collapsed until the shortcut fires.
    QWidget* m_findBar = nullptr;
    QLineEdit* m_findInput = nullptr;
    QLabel* m_findStatus = nullptr;

    void buildTweaksTab();
    // Takes the ListProfileIniFiles result so the target-file dropdown
    // can be populated at construction time. Building against m_handles
    // would be empty here because the editor tabs are built after this
    // call (see reload()).
    void buildResolutionTab(const std::vector<GrpcProfileIniFile>& files);
    void buildFindBar(QVBoxLayout* parentLayout);
    QPlainTextEdit* currentEditor() const;
    // Patches the two resolution keys (iWidth, iHeight) in [Display] of
    // the target file's current editor content. If the file isn't open
    // as an editor tab, writes through the daemon's SaveProfileIniFile RPC.
    void applyResolutionTo(const QString& filename, int width, int height);
};

} // namespace gorganizer
