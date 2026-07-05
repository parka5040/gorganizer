#pragma once

#include <QDialog>
#include <QList>
#include "GrpcClient.h"

class QListWidget;
class QLineEdit;
class QCheckBox;
class QPushButton;
class QLabel;

namespace gorganizer {

// ExecutablesDialog manages a game's MO2-style external tools (xEdit, LOOT,
// DynDOLOD, Nemesis/Pandora, BodySlide, …) and launches them through Proton
// against the mounted VFS.
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
    QLineEdit* m_args = nullptr;
    QLineEdit* m_workingDir = nullptr;
    QLineEdit* m_captureMod = nullptr;
    QLineEdit* m_extraRw = nullptr;
    QCheckBox* m_needsVfs = nullptr;
    QCheckBox* m_sanitizeEnv = nullptr;
    QLabel* m_formHint = nullptr;
    QPushButton* m_saveBtn = nullptr;
    QPushButton* m_removeBtn = nullptr;
    QPushButton* m_runBtn = nullptr;

    QString m_editingId; // empty => adding new
};

} // namespace gorganizer
