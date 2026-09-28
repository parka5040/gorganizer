#include "ModLoaderProgressDialog.h"
#include "ErrorPresenter.h"
#include "WindowFit.h"

#include <QDialogButtonBox>
#include <QLabel>
#include <QPlainTextEdit>
#include <QProgressBar>
#include <QPushButton>
#include <QRegularExpression>
#include <QVBoxLayout>

namespace gorganizer {

namespace {

constexpr int kDetailLimit = 400;
constexpr int kLogEntryLimit = 4000;
constexpr int kFailureLimit = 2000;

}

ModLoaderProgressDialog::ModLoaderProgressDialog(QWidget* parent)
    : QDialog(parent)
{
    setModal(false);

    auto* root = new QVBoxLayout(this);

    m_headline = new QLabel;
    m_headline->setTextFormat(Qt::PlainText);
    m_headline->setStyleSheet("font-weight: bold;");
    root->addWidget(m_headline);

    m_phase = new QLabel;
    m_phase->setTextFormat(Qt::PlainText);
    root->addWidget(m_phase);

    m_detail = new QLabel;
    m_detail->setTextFormat(Qt::PlainText);
    m_detail->setWordWrap(true);
    root->addWidget(m_detail);

    m_busy = new QProgressBar;
    m_busy->setRange(0, 0);
    m_busy->setTextVisible(false);
    root->addWidget(m_busy);

    m_log = new QPlainTextEdit;
    m_log->setReadOnly(true);
    m_log->setMaximumBlockCount(500);
    root->addWidget(m_log, 1);

    auto* hint = new QLabel("Hiding this window does not stop the operation; gorganizer reports the result when it "
                            "finishes. Quitting gorganizer while it runs can interrupt it.");
    hint->setTextFormat(Qt::PlainText);
    hint->setWordWrap(true);
    hint->setObjectName("hintLabel");
    root->addWidget(hint);

    auto* buttons = new QDialogButtonBox(QDialogButtonBox::Close);
    buttons->button(QDialogButtonBox::Close)->setText("Hide");
    buttons->button(QDialogButtonBox::Close)->setDefault(true);
    connect(buttons, &QDialogButtonBox::rejected, this, &QDialog::hide);
    root->addWidget(buttons);
    fitToScreen(this, QSize(520, 320));
}

void ModLoaderProgressDialog::begin(const QString& title, const QString& headline)
{
    m_active = true;
    m_lastFailure.clear();
    setWindowTitle(title);
    m_headline->setText(headline);
    m_phase->setText("Starting…");
    m_detail->clear();
    m_log->clear();
    show();
    raise();
    activateWindow();
}

void ModLoaderProgressDialog::finish()
{
    m_active = false;
    hide();
}

void ModLoaderProgressDialog::showNote(const QString& note)
{
    if (!m_active)
        return;
    m_phase->setText(capped(note, kDetailLimit));
    m_detail->clear();
    m_log->appendPlainText(capped(note, kLogEntryLimit));
}

void ModLoaderProgressDialog::onDaemonInfo(const QString& info)
{
    if (!m_active)
        return;
    static const QRegularExpression lineRe(QStringLiteral("^\\[smapi:([a-z]+)\\]\\s*(.*)$"),
                                           QRegularExpression::DotMatchesEverythingOption);
    const auto match = lineRe.match(info);
    if (!match.hasMatch())
        return;
    const QString phase = match.captured(1);
    const QString detail = match.captured(2).trimmed();
    if (phase == QLatin1String("failed"))
        m_lastFailure = capped(detail, kFailureLimit);
    const QString label = phaseLabel(phase);
    const QString shown = phase == QLatin1String("failed") || phase == QLatin1String("warning")
        ? errorSummary(QStringLiteral("change SMAPI"), detail, true) : detail;
    m_phase->setText(label);
    m_detail->setText(capped(shown, kDetailLimit));
    m_log->appendPlainText(shown.isEmpty() ? label : capped(QStringLiteral("%1: %2").arg(label, shown), kLogEntryLimit));
}

QString ModLoaderProgressDialog::capped(const QString& text, int limit)
{
    if (text.size() <= limit)
        return text;
    return text.left(limit) + QStringLiteral("…");
}

QString ModLoaderProgressDialog::phaseLabel(const QString& phase)
{
    if (phase == QLatin1String("download"))
        return QStringLiteral("Downloading");
    if (phase == QLatin1String("verify"))
        return QStringLiteral("Verifying");
    if (phase == QLatin1String("extract"))
        return QStringLiteral("Extracting");
    if (phase == QLatin1String("stage"))
        return QStringLiteral("Preparing a private copy of the game");
    if (phase == QLatin1String("run"))
        return QStringLiteral("Running the SMAPI installer");
    if (phase == QLatin1String("check"))
        return QStringLiteral("Checking the result");
    if (phase == QLatin1String("apply"))
        return QStringLiteral("Applying to the game folder");
    if (phase == QLatin1String("applied"))
        return QStringLiteral("Applied to the game folder");
    if (phase == QLatin1String("done"))
        return QStringLiteral("Done");
    if (phase == QLatin1String("failed"))
        return QStringLiteral("Failed");
    if (phase == QLatin1String("warning"))
        return QStringLiteral("Warning");
    return phase;
}

}
