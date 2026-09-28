#include "Dialogs.h"

#include <QPushButton>

namespace gorganizer::dialogs {

void info(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Information);
    box.setWindowTitle(title);
    box.setStandardButtons(QMessageBox::Ok);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.exec();
}

void warn(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Warning);
    box.setWindowTitle(title);
    box.setStandardButtons(QMessageBox::Ok);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.exec();
}

void error(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Critical);
    box.setWindowTitle(title);
    box.setStandardButtons(QMessageBox::Ok);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.exec();
}

void plainWarn(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Warning);
    box.setWindowTitle(title);
    box.setStandardButtons(QMessageBox::Ok);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.exec();
}

void plainInfo(QWidget* parent, const QString& title, const QString& text)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Information);
    box.setWindowTitle(title);
    box.setStandardButtons(QMessageBox::Ok);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.exec();
}

bool confirm(QWidget* parent, const QString& title, const QString& text,
             QMessageBox::StandardButton defaultButton,
             QMessageBox::StandardButton acceptButton,
             QMessageBox::StandardButton rejectButton)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Question);
    box.setWindowTitle(title);
    box.setStandardButtons(acceptButton | rejectButton);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.setDefaultButton(defaultButton);
    return box.exec() == acceptButton;
}

bool confirmWarn(QWidget* parent, const QString& title, const QString& text,
                 QMessageBox::StandardButton defaultButton)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Warning);
    box.setWindowTitle(title);
    box.setStandardButtons(QMessageBox::Yes | QMessageBox::No);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.setDefaultButton(defaultButton);
    return box.exec() == QMessageBox::Yes;
}

bool confirmDestructive(QWidget* parent, const QString& title, const QString& text,
                        const QString& acceptLabel, const QString& rejectLabel)
{
    QMessageBox box(parent);
    box.setWindowTitle(title);
    box.setIcon(QMessageBox::Warning);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    auto* acceptBtn = box.addButton(acceptLabel, QMessageBox::DestructiveRole);
    auto* cancelBtn = box.addButton(rejectLabel, QMessageBox::RejectRole);
    box.setDefaultButton(static_cast<QPushButton*>(cancelBtn));
    box.setEscapeButton(cancelBtn);
    box.exec();
    return box.clickedButton() == acceptBtn;
}

bool plainConfirm(QWidget* parent, const QString& title, const QString& text,
                  QMessageBox::Icon icon, QMessageBox::StandardButton defaultButton)
{
    QMessageBox box(parent);
    box.setIcon(icon);
    box.setWindowTitle(title);
    box.setStandardButtons(QMessageBox::Yes | QMessageBox::No);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    box.setDefaultButton(defaultButton);
    return box.exec() == QMessageBox::Yes;
}

bool plainConfirmDestructive(QWidget* parent, const QString& title, const QString& text,
                             const QString& acceptLabel, const QString& rejectLabel)
{
    QMessageBox box(parent);
    box.setWindowTitle(title);
    box.setIcon(QMessageBox::Warning);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    auto* acceptBtn = box.addButton(acceptLabel, QMessageBox::DestructiveRole);
    auto* cancelBtn = box.addButton(rejectLabel, QMessageBox::RejectRole);
    box.setDefaultButton(static_cast<QPushButton*>(cancelBtn));
    box.setEscapeButton(cancelBtn);
    box.exec();
    return box.clickedButton() == acceptBtn;
}

}
