#pragma once

#include <QMessageBox>
#include <QString>

class QWidget;

namespace gorganizer::dialogs {

// Shows an informational message as plain text.
void info(QWidget* parent, const QString& title, const QString& text);

// Shows a warning as plain text.
void warn(QWidget* parent, const QString& title, const QString& text);

// Shows an error as plain text.
void error(QWidget* parent, const QString& title, const QString& text);

// One-shot warning message box whose text always renders as plain text.
void plainWarn(QWidget* parent, const QString& title, const QString& text);

// One-shot informational message box whose text always renders as plain text.
void plainInfo(QWidget* parent, const QString& title, const QString& text);

// Two-button confirm with a question icon; returns true when acceptButton is chosen.
bool confirm(QWidget* parent, const QString& title, const QString& text,
             QMessageBox::StandardButton defaultButton = QMessageBox::NoButton,
             QMessageBox::StandardButton acceptButton = QMessageBox::Yes,
             QMessageBox::StandardButton rejectButton = QMessageBox::No);

// Two-button Yes/No confirm with a warning icon; returns true on Yes.
bool confirmWarn(QWidget* parent, const QString& title, const QString& text,
                 QMessageBox::StandardButton defaultButton = QMessageBox::NoButton);

// Two-button confirm whose accept button carries DestructiveRole styling; returns true when accepted.
bool confirmDestructive(QWidget* parent, const QString& title, const QString& text,
                        const QString& acceptLabel,
                        const QString& rejectLabel = QStringLiteral("Cancel"));

// Two-button Yes/No confirm whose text always renders as plain text; returns true on Yes.
bool plainConfirm(QWidget* parent, const QString& title, const QString& text,
                  QMessageBox::Icon icon = QMessageBox::Question,
                  QMessageBox::StandardButton defaultButton = QMessageBox::NoButton);

// Two-button confirm with DestructiveRole styling whose text always renders as plain text; returns true when accepted.
bool plainConfirmDestructive(QWidget* parent, const QString& title, const QString& text,
                             const QString& acceptLabel,
                             const QString& rejectLabel = QStringLiteral("Cancel"));

}
