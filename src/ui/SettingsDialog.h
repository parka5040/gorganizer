#pragma once

#include <QDialog>

class QLineEdit;
class QLabel;
class QPushButton;
class QComboBox;

namespace gorganizer {

class AppConfig;
class GrpcClient;

class SettingsDialog : public QDialog {
    Q_OBJECT
public:
    explicit SettingsDialog(GrpcClient* grpc, AppConfig* config, QWidget* parent = nullptr);

private slots:
    void onSaveKey();
    void onKeyValidated(bool valid, const QString& errorMessage);
    void onSaveProton();
    void onTestNxm();
    void onReregisterNxm();
    void onThemeChanged(const QString& name);

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
    QComboBox* m_themeCombo = nullptr;
};

} // namespace gorganizer
