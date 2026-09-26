#pragma once

#include <QDialog>
#include <QString>

class QLabel;
class QPlainTextEdit;
class QProgressBar;

namespace gorganizer {

class ModLoaderProgressDialog : public QDialog {
    Q_OBJECT
public:
    explicit ModLoaderProgressDialog(QWidget* parent = nullptr);

    // Resets the dialog for a new operation and shows it without blocking the main window.
    void begin(const QString& title, const QString& headline);
    // Stops following progress lines and hides the dialog.
    void finish();
    // Shows a status note of the controller, such as waiting for the daemon, as the current phase.
    void showNote(const QString& note);
    // Returns the detail of the last "failed" line seen during the current operation.
    QString lastFailure() const { return m_lastFailure; }

public slots:
    // Shows the phase and detail of a "[smapi:<phase>] <detail>" status line and ignores every other line.
    void onDaemonInfo(const QString& info);

private:
    // Returns the user-facing name of a progress phase.
    static QString phaseLabel(const QString& phase);
    // Returns text shortened to at most limit characters, marking the cut with an ellipsis.
    static QString capped(const QString& text, int limit);

    QLabel* m_headline = nullptr;
    QLabel* m_phase = nullptr;
    QLabel* m_detail = nullptr;
    QProgressBar* m_busy = nullptr;
    QPlainTextEdit* m_log = nullptr;
    bool m_active = false;
    QString m_lastFailure;
};

}
