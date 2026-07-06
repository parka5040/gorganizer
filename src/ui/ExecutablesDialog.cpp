#include "ExecutablesDialog.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QFormLayout>
#include <QListWidget>
#include <QLineEdit>
#include <QCheckBox>
#include <QPushButton>
#include <QLabel>
#include <QFileDialog>
#include <QMessageBox>
#include <QGroupBox>
#include <QFileInfo>

namespace gorganizer {

ExecutablesDialog::ExecutablesDialog(GrpcClient* grpc, const QString& gameId,
                                     const QString& profileName, QWidget* parent)
    : QDialog(parent), m_grpc(grpc), m_gameId(gameId), m_profileName(profileName)
{
    setWindowTitle("External Tools");
    resize(720, 460);

    auto* root = new QHBoxLayout(this);

    // Left: list + list actions.
    auto* leftCol = new QVBoxLayout;
    m_list = new QListWidget;
    connect(m_list, &QListWidget::currentRowChanged, this, &ExecutablesDialog::onSelectionChanged);
    leftCol->addWidget(m_list, 1);

    auto* listBtns = new QHBoxLayout;
    auto* addBtn = new QPushButton("Add");
    auto* detectBtn = new QPushButton("Detect installed…");
    connect(addBtn, &QPushButton::clicked, this, &ExecutablesDialog::onAddNew);
    connect(detectBtn, &QPushButton::clicked, this, &ExecutablesDialog::onDetect);
    listBtns->addWidget(addBtn);
    listBtns->addWidget(detectBtn);
    listBtns->addStretch();
    leftCol->addLayout(listBtns);
    root->addLayout(leftCol, 1);

    // Right: edit form.
    auto* formBox = new QGroupBox("Tool");
    auto* form = new QFormLayout(formBox);
    m_title = new QLineEdit;
    m_exePath = new QLineEdit;
    auto* browse = new QPushButton("Browse…");
    connect(browse, &QPushButton::clicked, this, [this] {
        QString f = QFileDialog::getOpenFileName(this, "Select executable", QString(),
                                                 "Executables (*.exe);;All files (*)");
        if (!f.isEmpty()) m_exePath->setText(f);
    });
    auto* exeRow = new QHBoxLayout;
    exeRow->addWidget(m_exePath, 1);
    exeRow->addWidget(browse);
    m_args = new QLineEdit;
    m_args->setPlaceholderText("space-separated; %DATA_DIR% %MODS_DIR% %OVERWRITE% %GAME_DIR%");
    m_workingDir = new QLineEdit;
    m_captureMod = new QLineEdit;
    m_captureMod->setPlaceholderText("(blank = Overwrite)");
    m_extraRw = new QLineEdit;
    m_extraRw->setPlaceholderText("extra writable paths, comma-separated (optional)");
    m_needsVfs = new QCheckBox("Needs the mod view mounted");
    m_needsVfs->setChecked(true);
    m_sanitizeEnv = new QCheckBox("Sanitize environment (recommended)");
    m_sanitizeEnv->setChecked(true);

    form->addRow("Title", m_title);
    form->addRow("Executable", exeRow);
    form->addRow("Arguments", m_args);
    form->addRow("Working dir", m_workingDir);
    form->addRow("Capture output to mod", m_captureMod);
    form->addRow("Extra RW paths", m_extraRw);
    form->addRow(m_needsVfs);
    form->addRow(m_sanitizeEnv);

    m_formHint = new QLabel;
    m_formHint->setWordWrap(true);
    m_formHint->setObjectName("hintLabel");
    form->addRow(m_formHint);

    auto* rightCol = new QVBoxLayout;
    rightCol->addWidget(formBox, 1);

    auto* formBtns = new QHBoxLayout;
    m_saveBtn = new QPushButton("Save");
    m_removeBtn = new QPushButton("Remove");
    m_runBtn = new QPushButton("Run");
    connect(m_saveBtn, &QPushButton::clicked, this, &ExecutablesDialog::onSave);
    connect(m_removeBtn, &QPushButton::clicked, this, &ExecutablesDialog::onRemove);
    connect(m_runBtn, &QPushButton::clicked, this, &ExecutablesDialog::onRun);
    formBtns->addWidget(m_runBtn);
    formBtns->addStretch();
    formBtns->addWidget(m_removeBtn);
    formBtns->addWidget(m_saveBtn);
    rightCol->addLayout(formBtns);

    auto* closeRow = new QHBoxLayout;
    closeRow->addStretch();
    auto* closeBtn = new QPushButton("Close");
    connect(closeBtn, &QPushButton::clicked, this, &QDialog::accept);
    closeRow->addWidget(closeBtn);
    rightCol->addLayout(closeRow);

    root->addLayout(rightCol, 2);

    reload();
    clearForm();
}

int ExecutablesDialog::currentIndex() const
{
    return m_list ? m_list->currentRow() : -1;
}

void ExecutablesDialog::reload()
{
    QString err;
    m_executables.clear();
    if (!m_grpc->listExecutables(m_gameId, m_executables, err)) {
        QMessageBox::warning(this, "Tools", QString("Could not load tools: %1").arg(err));
    }
    m_list->clear();
    for (const auto& e : m_executables) {
        QString label = e.title;
        if (e.autoDetected) label += "  (detected)";
        m_list->addItem(label);
    }
}

void ExecutablesDialog::onSelectionChanged()
{
    int i = currentIndex();
    if (i < 0 || i >= m_executables.size()) return;
    loadIntoForm(m_executables[i]);
}

void ExecutablesDialog::loadIntoForm(const GrpcExecutable& e)
{
    m_editingId = e.id;
    m_title->setText(e.title);
    m_exePath->setText(e.exePath);
    m_args->setText(e.args.join(' '));
    m_workingDir->setText(e.workingDir);
    m_captureMod->setText(e.captureOutputToMod);
    m_extraRw->setText(e.extraRwPaths.join(", "));
    m_needsVfs->setChecked(e.needsVfsMounted);
    m_sanitizeEnv->setChecked(e.sanitizeEnv);
    m_removeBtn->setEnabled(true);
    m_runBtn->setEnabled(true);
    m_formHint->setText(QString("Editing \"%1\".").arg(e.title));
}

void ExecutablesDialog::clearForm()
{
    m_editingId.clear();
    m_title->clear();
    m_exePath->clear();
    m_args->clear();
    m_workingDir->clear();
    m_captureMod->clear();
    m_extraRw->clear();
    m_needsVfs->setChecked(true);
    m_sanitizeEnv->setChecked(true);
    m_removeBtn->setEnabled(false);
    m_runBtn->setEnabled(false);
    m_formHint->setText("Add a new tool, then Save.");
}

GrpcExecutable ExecutablesDialog::formToExecutable() const
{
    GrpcExecutable e;
    e.id = m_editingId;
    e.title = m_title->text().trimmed();
    e.exePath = m_exePath->text().trimmed();
    const QString args = m_args->text().trimmed();
    if (!args.isEmpty())
        e.args = args.split(' ', Qt::SkipEmptyParts);
    e.workingDir = m_workingDir->text().trimmed();
    e.captureOutputToMod = m_captureMod->text().trimmed();
    const QString rw = m_extraRw->text().trimmed();
    if (!rw.isEmpty())
        for (const QString& p : rw.split(',', Qt::SkipEmptyParts))
            e.extraRwPaths << p.trimmed();
    e.needsVfsMounted = m_needsVfs->isChecked();
    e.sanitizeEnv = m_sanitizeEnv->isChecked();
    return e;
}

void ExecutablesDialog::onAddNew()
{
    m_list->setCurrentRow(-1);
    clearForm();
    m_title->setFocus();
}

void ExecutablesDialog::onSave()
{
    GrpcExecutable e = formToExecutable();
    if (e.title.isEmpty() || e.exePath.isEmpty()) {
        QMessageBox::warning(this, "Tools", "A title and an executable path are required.");
        return;
    }
    QString err;
    GrpcExecutable saved;
    if (!m_grpc->upsertExecutable(m_gameId, e, saved, err)) {
        QMessageBox::warning(this, "Tools", QString("Could not save: %1").arg(err));
        return;
    }
    reload();
    // Re-select the saved item.
    for (int i = 0; i < m_executables.size(); ++i) {
        if (m_executables[i].id == saved.id) { m_list->setCurrentRow(i); break; }
    }
}

void ExecutablesDialog::onRemove()
{
    if (m_editingId.isEmpty()) return;
    if (QMessageBox::question(this, "Remove tool", "Remove this tool from the list?")
        != QMessageBox::Yes)
        return;
    QString err;
    if (!m_grpc->removeExecutable(m_gameId, m_editingId, err)) {
        QMessageBox::warning(this, "Tools", QString("Could not remove: %1").arg(err));
        return;
    }
    reload();
    clearForm();
}

void ExecutablesDialog::onDetect()
{
    QString err;
    QList<GrpcDetectedExecutable> found;
    if (!m_grpc->detectExecutables(m_gameId, found, err)) {
        QMessageBox::warning(this, "Detect tools", QString("Detection failed: %1").arg(err));
        return;
    }
    if (found.isEmpty()) {
        QMessageBox::information(this, "Detect tools",
            "No known tools found in the game's data or enabled mods.");
        return;
    }
    // Add any detected tool not already registered (matched by exe path).
    int added = 0;
    for (const auto& d : found) {
        bool exists = false;
        for (const auto& e : m_executables)
            if (e.exePath == d.exePath) { exists = true; break; }
        if (exists) continue;
        GrpcExecutable e;
        e.title = d.title;
        e.exePath = d.exePath;
        e.needsVfsMounted = d.needsVfsMounted;
        e.captureOutputToMod = d.captureOutputToMod;
        e.sanitizeEnv = true;
        e.autoDetected = true;
        GrpcExecutable saved;
        QString serr;
        if (m_grpc->upsertExecutable(m_gameId, e, saved, serr)) added++;
    }
    reload();
    QMessageBox::information(this, "Detect tools",
        QString("Found %1 tool(s); added %2 new.").arg(found.size()).arg(added));
}

void ExecutablesDialog::onRun()
{
    if (m_editingId.isEmpty()) {
        QMessageBox::information(this, "Run tool", "Save the tool first, then Run.");
        return;
    }
    m_runBtn->setEnabled(false);
    int pid = 0;
    QString runId, err;
    bool ok = m_grpc->launchExecutable(m_gameId, m_editingId, m_profileName, pid, runId, err);
    m_runBtn->setEnabled(true);
    if (!ok) {
        QMessageBox::warning(this, "Run tool", QString("Launch failed:\n\n%1").arg(err));
        return;
    }
    QMessageBox::information(this, "Run tool",
        QString("Launched (PID %1).\nOutput will be captured into the tool's mod when it exits.")
            .arg(pid));
}

} // namespace gorganizer
