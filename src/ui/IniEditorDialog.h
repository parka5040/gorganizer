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
    QComboBox* m_resolutionTarget = nullptr;
    QLabel* m_resolutionStatus = nullptr;

    QWidget* m_findBar = nullptr;
    QLineEdit* m_findInput = nullptr;
    QLabel* m_findStatus = nullptr;

    void buildTweaksTab();
    // Built at construction time from ListProfileIniFiles since editor tabs come later.
    void buildResolutionTab(const std::vector<GrpcProfileIniFile>& files);
    void buildFindBar(QVBoxLayout* parentLayout);
    QPlainTextEdit* currentEditor() const;
    // Patches iWidth/iHeight in [Display] of the target file's editor or via SaveProfileIniFile.
    void applyResolutionTo(const QString& filename, int width, int height);
};

}
