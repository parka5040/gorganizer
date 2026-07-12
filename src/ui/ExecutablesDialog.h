#pragma once

#include <QDialog>
#include <QList>
#include "GrpcClient.h"

class QListWidget;
class QLineEdit;
class QPlainTextEdit;
class QCheckBox;
class QPushButton;
class QLabel;
class QComboBox;
class QSpinBox;

namespace gorganizer {

class ExecutablesDialog : public QDialog {
    Q_OBJECT
public:
    ExecutablesDialog(GrpcClient* grpc, const QString& gameId, const QString& profileName,
                      QWidget* parent = nullptr);

private slots:
    void onSelectionChanged();
    void onAddNew();
    void onSave();
    void onRemove();
    void onDetect();
    void onRun();
    void onSortLOOT();
    void onInstallLOOT();
    void onRollbackLOOT();

private:
    void reload();
    void loadIntoForm(const GrpcExecutable& e);
    GrpcExecutable formToExecutable() const;
    void clearForm();
    int currentIndex() const;

    GrpcClient* m_grpc;
    QString m_gameId;
    QString m_profileName;
    QList<GrpcExecutable> m_executables;

    QListWidget* m_list = nullptr;
    QLineEdit* m_title = nullptr;
    QLineEdit* m_exePath = nullptr;
    QPlainTextEdit* m_args = nullptr;
    QPlainTextEdit* m_environment = nullptr;
    QLineEdit* m_workingDir = nullptr;
    QLineEdit* m_captureMod = nullptr;
    QLineEdit* m_extraRw = nullptr;
    QLineEdit* m_selectedInput = nullptr;
    QComboBox* m_runner = nullptr;
    QComboBox* m_outputPolicy = nullptr;
    QSpinBox* m_prefixAppId = nullptr;
    QCheckBox* m_needsVfs = nullptr;
    QCheckBox* m_sanitizeEnv = nullptr;
    QLabel* m_formHint = nullptr;
    QPushButton* m_saveBtn = nullptr;
    QPushButton* m_removeBtn = nullptr;
    QPushButton* m_runBtn = nullptr;
    QPushButton* m_sortBtn = nullptr;

    QString m_editingId;
    QString m_toolId;
};

}
