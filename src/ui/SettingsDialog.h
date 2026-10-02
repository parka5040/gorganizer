#pragma once

#include <QDialog>

class QLineEdit;
class QLabel;
class QPushButton;
class QComboBox;
class QCheckBox;
class QProcess;

namespace gorganizer {

class AppConfig;
class GrpcClient;

class SettingsDialog : public QDialog {
    Q_OBJECT
public:
    explicit SettingsDialog(GrpcClient* grpc, AppConfig* config, QWidget* parent = nullptr);

signals:
    void collapsedSeparatorViewChanged(bool on);

private slots:
    void onSaveKey();
    void onKeyValidated(bool valid, const QString& errorMessage);
    void onSaveProton();
    void onTestNxm();
    void onReregisterNxm();
    void onThemeChanged(const QString& name);
    void onCollapsedSeparatorViewToggled(bool on);
    // Saves the explicit startup update-check preference.
    void onUpdateCheckToggled(bool on);

private:
    void populateProtonCombo();
    void populateThemeCombo();

    GrpcClient* m_grpc;
    AppConfig* m_config = nullptr;
    QLineEdit* m_apiKeyEdit;
    QLabel* m_statusLabel;
    QPushButton* m_saveBtn = nullptr;
    QComboBox* m_protonCombo = nullptr;
    QLabel* m_protonStatus = nullptr;
    QLabel* m_nxmStatus = nullptr;
    QPushButton* m_reregNxmBtn = nullptr;
    QProcess* m_nxmRegisterProcess = nullptr;
    QComboBox* m_themeCombo = nullptr;
    QCheckBox* m_collapseViewsCheck = nullptr;
    QCheckBox* m_updateCheck = nullptr;
    QLabel* m_updateStatus = nullptr;
};

}
