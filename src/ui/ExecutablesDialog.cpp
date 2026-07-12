#include "ExecutablesDialog.h"
#include "Dialogs.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QFormLayout>
#include <QListWidget>
#include <QLineEdit>
#include <QPlainTextEdit>
#include <QCheckBox>
#include <QPushButton>
#include <QLabel>
#include <QFileDialog>
#include <QGroupBox>
#include <QFileInfo>
#include <QComboBox>
#include <QSpinBox>

namespace gorganizer {

ExecutablesDialog::ExecutablesDialog(GrpcClient* grpc, const QString& gameId,
                                     const QString& profileName, QWidget* parent)
    : QDialog(parent), m_grpc(grpc), m_gameId(gameId), m_profileName(profileName)
{
    setWindowTitle("External Tools");
    resize(920, 700);

    auto* root = new QHBoxLayout(this);

    auto* leftCol = new QVBoxLayout;
    m_list = new QListWidget;
    connect(m_list, &QListWidget::currentRowChanged, this, &ExecutablesDialog::onSelectionChanged);
    leftCol->addWidget(m_list, 1);

    auto* listBtns = new QHBoxLayout;
    auto* addBtn = new QPushButton("Add");
    auto* detectBtn = new QPushButton("Detect installed…");
    auto* installLootBtn = new QPushButton("Install/Update LOOT…");
    auto* rollbackLootBtn = new QPushButton("Rollback LOOT");
    connect(addBtn, &QPushButton::clicked, this, &ExecutablesDialog::onAddNew);
    connect(detectBtn, &QPushButton::clicked, this, &ExecutablesDialog::onDetect);
    connect(installLootBtn, &QPushButton::clicked, this, &ExecutablesDialog::onInstallLOOT);
    connect(rollbackLootBtn, &QPushButton::clicked, this, &ExecutablesDialog::onRollbackLOOT);
    listBtns->addWidget(addBtn);
    listBtns->addWidget(detectBtn);
    listBtns->addStretch();
    leftCol->addLayout(listBtns);
    leftCol->addWidget(installLootBtn);
    leftCol->addWidget(rollbackLootBtn);
    root->addLayout(leftCol, 1);

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
    m_args = new QPlainTextEdit;
    m_args->setPlaceholderText("One argument per line; %DATA_DIR% %PROFILE_DIR% %OUTPUT_DIR% %GAME_DIR%");
    m_args->setMaximumHeight(90);
    m_environment = new QPlainTextEdit;
    m_environment->setPlaceholderText("One NAME=value entry per line");
    m_environment->setMaximumHeight(70);
    m_workingDir = new QLineEdit;
    m_captureMod = new QLineEdit;
    m_captureMod->setPlaceholderText("(blank = Overwrite)");
    m_extraRw = new QLineEdit;
    m_extraRw->setPlaceholderText("extra writable paths, comma-separated (optional)");
    m_needsVfs = new QCheckBox("Needs the mod view mounted");
    m_needsVfs->setChecked(true);
    m_sanitizeEnv = new QCheckBox("Sanitize environment (recommended)");
    m_sanitizeEnv->setChecked(true);
    m_selectedInput = new QLineEdit;
    m_selectedInput->setPlaceholderText("Relative Data path for copy-up tools");
    m_runner = new QComboBox;
    m_runner->addItem("Proton", "proton");
    m_runner->addItem("Native", "native");
    m_runner->addItem("Java", "java");
    m_outputPolicy = new QComboBox;
    for (const auto& policy : QStringList{"none", "read_only", "profile_sync", "scratch_import",
                                          "selected_copy_up", "named_output_mod", "exclusive_source_edit"})
        m_outputPolicy->addItem(policy, policy);
    m_prefixAppId = new QSpinBox;
    m_prefixAppId->setRange(0, 2147483647);
    m_prefixAppId->setSpecialValueText("Game prefix");

    form->addRow("Title", m_title);
    form->addRow("Executable", exeRow);
    form->addRow("Arguments", m_args);
    form->addRow("Environment", m_environment);
    form->addRow("Working dir", m_workingDir);
    form->addRow("Runner", m_runner);
    form->addRow("Prefix app ID", m_prefixAppId);
    form->addRow("Output policy", m_outputPolicy);
    form->addRow("Selected input", m_selectedInput);
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
    m_sortBtn = new QPushButton("Sort with LOOT");
    connect(m_saveBtn, &QPushButton::clicked, this, &ExecutablesDialog::onSave);
    connect(m_removeBtn, &QPushButton::clicked, this, &ExecutablesDialog::onRemove);
    connect(m_runBtn, &QPushButton::clicked, this, &ExecutablesDialog::onRun);
    connect(m_sortBtn, &QPushButton::clicked, this, &ExecutablesDialog::onSortLOOT);
    formBtns->addWidget(m_runBtn);
    formBtns->addWidget(m_sortBtn);
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
        dialogs::warn(this, "Tools", QString("Could not load tools: %1").arg(err));
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
    m_toolId = e.toolId;
    m_title->setText(e.title);
    m_exePath->setText(e.exePath);
    m_args->setPlainText(e.args.join('\n'));
    QStringList environment;
    for (auto it = e.environment.cbegin(); it != e.environment.cend(); ++it)
        environment << it.key() + "=" + it.value();
    m_environment->setPlainText(environment.join('\n'));
    m_workingDir->setText(e.workingDir);
    m_captureMod->setText(e.captureOutputToMod);
    m_extraRw->setText(e.extraRwPaths.join(", "));
    m_selectedInput->setText(e.selectedInput);
    int runnerIndex = m_runner->findData(e.runner.isEmpty() ? "proton" : e.runner);
    m_runner->setCurrentIndex(qMax(0, runnerIndex));
    int policyIndex = m_outputPolicy->findData(e.outputPolicy.isEmpty() ? "none" : e.outputPolicy);
    m_outputPolicy->setCurrentIndex(qMax(0, policyIndex));
    m_prefixAppId->setValue(e.prefixAppId);
    m_needsVfs->setChecked(e.needsVfsMounted);
    m_sanitizeEnv->setChecked(e.sanitizeEnv);
    m_removeBtn->setEnabled(true);
    m_runBtn->setEnabled(true);
    m_sortBtn->setEnabled(e.toolId == "loot" && m_gameId != "ttw");
    m_formHint->setText(QString("Editing \"%1\".").arg(e.title));
}

void ExecutablesDialog::clearForm()
{
    m_editingId.clear();
    m_toolId.clear();
    m_title->clear();
    m_exePath->clear();
    m_args->clear();
    m_environment->clear();
    m_workingDir->clear();
    m_captureMod->clear();
    m_extraRw->clear();
    m_selectedInput->clear();
    m_runner->setCurrentIndex(0);
    m_outputPolicy->setCurrentIndex(0);
    m_prefixAppId->setValue(0);
    m_needsVfs->setChecked(true);
    m_sanitizeEnv->setChecked(true);
    m_removeBtn->setEnabled(false);
    m_runBtn->setEnabled(false);
    m_sortBtn->setEnabled(false);
    m_formHint->setText("Add a new tool, then Save.");
}

GrpcExecutable ExecutablesDialog::formToExecutable() const
{
    GrpcExecutable e;
    e.id = m_editingId;
    e.toolId = m_toolId;
    e.title = m_title->text().trimmed();
    e.exePath = m_exePath->text().trimmed();
    const QString args = m_args->toPlainText().trimmed();
    if (!args.isEmpty())
        for (const QString& arg : args.split('\n', Qt::SkipEmptyParts)) e.args << arg.trimmed();
    for (const QString& line : m_environment->toPlainText().split('\n', Qt::SkipEmptyParts)) {
        const int equals = line.indexOf('=');
        if (equals > 0) e.environment.insert(line.left(equals).trimmed(), line.mid(equals + 1));
    }
    e.workingDir = m_workingDir->text().trimmed();
    e.captureOutputToMod = m_captureMod->text().trimmed();
    const QString rw = m_extraRw->text().trimmed();
    if (!rw.isEmpty()) {
        for (const QString& p : rw.split(',', Qt::SkipEmptyParts))
            e.extraRwPaths << p.trimmed();
    }
    e.selectedInput = m_selectedInput->text().trimmed();
    e.runner = m_runner->currentData().toString();
    e.outputPolicy = m_outputPolicy->currentData().toString();
    e.prefixAppId = m_prefixAppId->value();
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
        dialogs::warn(this, "Tools", "A title and an executable path are required.");
        return;
    }
    QString err;
    GrpcExecutable saved;
    if (!m_grpc->upsertExecutable(m_gameId, e, saved, err)) {
        dialogs::warn(this, "Tools", QString("Could not save: %1").arg(err));
        return;
    }
    reload();
    for (int i = 0; i < m_executables.size(); ++i) {
        if (m_executables[i].id == saved.id) { m_list->setCurrentRow(i); break; }
    }
}

void ExecutablesDialog::onRemove()
{
    if (m_editingId.isEmpty()) return;
    if (!dialogs::confirm(this, "Remove tool", "Remove this tool from the list?"))
        return;
    QString err;
    if (!m_grpc->removeExecutable(m_gameId, m_editingId, err)) {
        dialogs::warn(this, "Tools", QString("Could not remove: %1").arg(err));
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
        dialogs::warn(this, "Detect tools", QString("Detection failed: %1").arg(err));
        return;
    }
    if (found.isEmpty()) {
        dialogs::info(this, "Detect tools",
            "No known tools found in the game's data or enabled mods.");
        return;
    }
    int added = 0;
    for (const auto& d : found) {
        bool exists = false;
        for (const auto& e : m_executables)
            if (e.exePath == d.exePath) { exists = true; break; }
        if (exists) continue;
        GrpcExecutable e;
        e.title = d.title;
        e.toolId = d.toolId;
        e.exePath = d.exePath;
        e.runner = d.runner;
        e.prefixAppId = d.prefixAppId;
        e.outputPolicy = d.outputPolicy;
        e.args = d.defaultArgs;
        e.needsVfsMounted = d.needsVfsMounted;
        e.captureOutputToMod = d.captureOutputToMod;
        e.sanitizeEnv = true;
        e.autoDetected = true;
        GrpcExecutable saved;
        QString serr;
        if (m_grpc->upsertExecutable(m_gameId, e, saved, serr)) added++;
    }
    reload();
    dialogs::info(this, "Detect tools",
        QString("Found %1 tool(s); added %2 new.").arg(found.size()).arg(added));
}

void ExecutablesDialog::onRun()
{
    if (m_editingId.isEmpty()) {
        dialogs::info(this, "Run tool", "Save the tool first, then Run.");
        return;
    }
    m_runBtn->setEnabled(false);
    int pid = 0;
    QString runId, err;
    bool ok = m_grpc->launchExecutable(m_gameId, m_editingId, m_profileName, pid, runId, err, false);
    m_runBtn->setEnabled(true);
    if (!ok) {
        dialogs::warn(this, "Run tool", QString("Launch failed:\n\n%1").arg(err));
        return;
    }
    dialogs::info(this, "Run tool",
        QString("Launched (PID %1).\nOutput will be captured into the tool's mod when it exits.")
            .arg(pid));
}

void ExecutablesDialog::onSortLOOT()
{
    if (m_toolId != "loot" || m_gameId == "ttw") return;
    m_sortBtn->setEnabled(false);
    int pid = 0;
    QString runId, err;
    const bool ok = m_grpc->launchExecutable(m_gameId, m_editingId, m_profileName,
                                              pid, runId, err, true);
    m_sortBtn->setEnabled(true);
    if (!ok) dialogs::warn(this, "Sort with LOOT", QString("Launch failed:\n\n%1").arg(err));
}

void ExecutablesDialog::onInstallLOOT()
{
    GrpcManagedToolStatus status;
    QString err;
    if (!m_grpc->getManagedToolStatus("loot", status, err)) {
        dialogs::warn(this, "LOOT", QString("Could not read LOOT status: %1").arg(err));
        return;
    }
    const QString prompt = status.installed
        ? QString("LOOT %1 is installed. Check for and install the latest stable release?").arg(status.activeVersion)
        : "Download and install the latest stable Windows portable LOOT release from GitHub?";
    if (!dialogs::confirm(this, "Managed LOOT", prompt)) return;
    if (!m_grpc->installManagedTool("loot", status, err)) {
        dialogs::warn(this, "LOOT", QString("Installation failed: %1").arg(err));
        return;
    }
    reload();
    dialogs::info(this, "LOOT", QString("LOOT %1 is ready.").arg(status.activeVersion));
}

void ExecutablesDialog::onRollbackLOOT()
{
    if (!dialogs::confirm(this, "Rollback LOOT", "Reactivate the previously installed LOOT version?")) return;
    GrpcManagedToolStatus status;
    QString err;
    if (!m_grpc->rollbackManagedTool("loot", status, err)) {
        dialogs::warn(this, "LOOT", QString("Rollback failed: %1").arg(err));
        return;
    }
    reload();
    dialogs::info(this, "LOOT", QString("LOOT %1 is active.").arg(status.activeVersion));
}

}
