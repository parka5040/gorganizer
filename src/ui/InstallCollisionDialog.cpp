#include "InstallCollisionDialog.h"
#include "Dialogs.h"
#include "InstallErrorText.h"

#include <QAbstractButton>
#include <QInputDialog>
#include <QLineEdit>
#include <QMessageBox>
#include <QPushButton>

namespace gorganizer {

QString askInstallModName(QWidget* parent, const QString& title, const QString& label,
                          const QString& initial)
{
    QString name = initial;
    for (;;) {
        bool ok = false;
        name = QInputDialog::getText(parent, title, label, QLineEdit::Normal, name, &ok).trimmed();
        if (!ok)
            return {};
        const QString problem = modNameProblem(name);
        if (problem.isEmpty())
            return name;
        dialogs::plainWarn(parent, title, problem);
    }
}

std::optional<InstallCollisionChoice> resolveInstallCollision(QWidget* parent,
                                                               const QString& error,
                                                               const QString& fallbackName)
{
    const InstallError parsed = parseInstallError(error);
    QString existingName = parsed.fields.value(QStringLiteral("name"));
    if (existingName.isEmpty())
        existingName = parsed.fields.value(QStringLiteral("existing")).section(QLatin1Char(','), 0, 0);
    if (existingName.isEmpty())
        existingName = fallbackName;

    QMessageBox box(parent);
    box.setWindowTitle("Mod Already Exists");
    box.setIcon(QMessageBox::Question);
    box.setTextFormat(Qt::PlainText);
    box.setText(QString("A mod named \"%1\" is already installed.").arg(existingName));
    box.setInformativeText("Replace installs this archive as the new version of that mod and keeps its settings. "
                           "Merge adds this archive on top of the existing files, so files an update removed "
                           "stay behind. Install separately keeps both.");
    QAbstractButton* replaceBtn = box.addButton("Replace (recommended for updates)", QMessageBox::AcceptRole);
    QAbstractButton* mergeBtn = box.addButton("Merge into existing (keeps old files)", QMessageBox::ActionRole);
    QAbstractButton* separateBtn = box.addButton("Install as a separate mod…", QMessageBox::ActionRole);
    box.addButton("Cancel", QMessageBox::RejectRole);
    box.setDefaultButton(static_cast<QPushButton*>(replaceBtn));
    box.exec();

    if (box.clickedButton() == replaceBtn)
        return InstallCollisionChoice{GrpcInstallReplaceMod, existingName};
    if (box.clickedButton() == mergeBtn)
        return InstallCollisionChoice{GrpcInstallMergeIntoMod, existingName};
    if (box.clickedButton() == separateBtn) {
        const QString name = askInstallModName(parent, "Install Mod", "New mod name:", existingName);
        if (!name.isEmpty())
            return InstallCollisionChoice{GrpcInstallAsNewMod, name};
    }
    return std::nullopt;
}

}
