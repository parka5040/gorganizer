#pragma once

#include <QMessageBox>
#include <QString>

class QWidget;

namespace gorganizer::dialogs {

// One-shot informational message box.
void info(QWidget* parent, const QString& title, const QString& text);

// One-shot warning message box.
void warn(QWidget* parent, const QString& title, const QString& text);

// One-shot error message box.
void error(QWidget* parent, const QString& title, const QString& text);

// One-shot warning message box whose text always renders as rich text.
void richWarn(QWidget* parent, const QString& title, const QString& text);

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

}
