#include "Dialogs.h"

#include <QPushButton>

namespace gorganizer::dialogs {

void info(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox::information(parent, title, text);
}

void warn(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox::warning(parent, title, text);
}

void error(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox::critical(parent, title, text);
}

void richWarn(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox box(QMessageBox::Warning, title, text, QMessageBox::Ok, parent);
    box.setTextFormat(Qt::RichText);
    box.exec();
}

bool confirm(QWidget* parent, const QString& title, const QString& text,
             QMessageBox::StandardButton defaultButton,
             QMessageBox::StandardButton acceptButton,
             QMessageBox::StandardButton rejectButton)
{
    return QMessageBox::question(parent, title, text,
                                 acceptButton | rejectButton, defaultButton)
        == acceptButton;
}

bool confirmWarn(QWidget* parent, const QString& title, const QString& text,
                 QMessageBox::StandardButton defaultButton)
{
    return QMessageBox::warning(parent, title, text,
                                QMessageBox::Yes | QMessageBox::No, defaultButton)
        == QMessageBox::Yes;
}

bool confirmDestructive(QWidget* parent, const QString& title, const QString& text,
                        const QString& acceptLabel, const QString& rejectLabel)
{
    QMessageBox box(parent);
    box.setWindowTitle(title);
    box.setIcon(QMessageBox::Warning);
    box.setText(text);
    auto* acceptBtn = box.addButton(acceptLabel, QMessageBox::DestructiveRole);
    box.addButton(rejectLabel, QMessageBox::RejectRole);
    box.exec();
    return box.clickedButton() == acceptBtn;
}

}
