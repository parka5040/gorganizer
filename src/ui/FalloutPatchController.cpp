#include "FalloutPatchController.h"
#include "GrpcClient.h"
#include "SessionController.h"
#include "RunButtonWidget.h"
#include "Dialogs.h"

#include <QAction>
#include <QMessageBox>
#include <QStatusBar>

namespace gorganizer {

FalloutPatchController::FalloutPatchController(GrpcClient* grpc, SessionController* session,
                                               RunButtonWidget* runButton, QAction* patchAction,
                                               QStatusBar* statusBar, QWidget* parentWindow)
    : QObject(parentWindow)
    , m_grpc(grpc)
    , m_session(session)
    , m_runButton(runButton)
    , m_patchAction(patchAction)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
}

void FalloutPatchController::onActiveGameChanged(const GameInfo& game)
{
    const bool isFNV = (game.shortName == "falloutnv" && game.detected);
    const bool isTTW = (game.shortName == "ttw" && game.detected);
    if (m_patchAction)
        m_patchAction->setVisible(isFNV || isTTW);
    bool patched = false;
    if ((isFNV || isTTW) && m_grpc->isConnected())
        patched = m_grpc->is4GBPatched(game.shortName);
    m_runButton->setFourGBPatched(patched);
    if (m_patchAction && patched) {
        m_patchAction->setEnabled(false);
        m_patchAction->setToolTip(
            "FalloutNV.exe is already patched to 4GB. Re-running the patcher is unnecessary.");
    } else if (m_patchAction) {
        m_patchAction->setEnabled(true);
        m_patchAction->setToolTip(QString());
    }
}

void FalloutPatchController::onPatchFalloutTo4GB()
{
    const bool isFNV = (m_session->activeGame().shortName == "falloutnv");
    const bool isTTW = (m_session->activeGame().shortName == "ttw");
    if ((!isFNV && !isTTW) || !m_session->activeGame().detected) {
        dialogs::info(m_parentWindow, "Patch Fallout to 4GB",
            "This patch is only available for Fallout: New Vegas (or "
            "Tale of Two Wastelands, which shares FNV's install).");
        return;
    }
    if (!m_grpc->isConnected()) {
        dialogs::warn(m_parentWindow, "Not Connected",
            "The daemon must be running to download the 4GB patcher.");
        return;
    }

    auto confirm = QMessageBox::question(m_parentWindow, "Patch Fallout to 4GB",
        "<p>The 4GB Patcher modifies FalloutNV.exe in place so the game can "
        "address more than 2 GiB of memory — required for heavy mod load orders.</p>"
        "<p>Requirements:</p>"
        "<ul>"
        "<li>xNVSE must already be installed.</li>"
        "<li>A Nexus API key must be configured in Tools &#x2192; Settings.</li>"
        "</ul>"
        "<p>Continue?</p>",
        QMessageBox::Yes | QMessageBox::No, QMessageBox::Yes);
    if (confirm != QMessageBox::Yes)
        return;

    m_statusBar->showMessage("Downloading FNV 4GB patcher from Nexus...");
    QString patcherExePath, version, err;
    if (!m_grpc->install4GBPatcher(m_session->activeGame().shortName, patcherExePath, version, err)) {
        const QString lower = err.toLower();
        if (lower.contains("xnvse")) {
            dialogs::warn(m_parentWindow, "xNVSE Required",
                "<p>xNVSE must be installed before applying the 4GB patch. "
                "The patcher relies on the script extender being in place.</p>"
                "<p>Open the Run combo and choose <b>Install xNVSE...</b>, then try "
                "again.</p>");
        } else if (lower.contains("api key") || lower.contains("apikey")) {
            dialogs::warn(m_parentWindow, "Nexus API Key Required",
                "<p>A Nexus Mods API key is required to download the 4GB patcher.</p>"
                "<p>Open <b>Tools &#x2192; Settings</b> and paste a key, then try "
                "again.</p>");
        } else {
            dialogs::warn(m_parentWindow, "Download Failed",
                QString("<p>%1</p>"
                        "<p>If you are a non-premium Nexus user, open the mod page "
                        "in a browser and click 'Download with Manager' to trigger "
                        "an NXM download.</p>").arg(err.toHtmlEscaped()));
        }
        m_statusBar->clearMessage();
        return;
    }

    m_statusBar->showMessage(
        QString("FNV 4GB patcher %1 downloaded.").arg(version), 5000);

    auto apply = QMessageBox::question(m_parentWindow, "Apply 4GB Patch",
        QString("<p>The patcher has been extracted to:</p>"
                "<p><code>%1</code></p>"
                "<p>Apply the patch to <b>FalloutNV.exe</b> now? "
                "This rewrites the game executable in place.</p>")
            .arg(patcherExePath.toHtmlEscaped()),
        QMessageBox::Yes | QMessageBox::No, QMessageBox::Yes);
    if (apply != QMessageBox::Yes) {
        m_statusBar->showMessage(
            "Patcher downloaded; apply it later from Tools \xE2\x86\x92 Patch Fallout to 4GB.",
            8000);
        return;
    }

    m_statusBar->showMessage("Applying 4GB patch...");
    QString output, applyErr;
    if (!m_grpc->apply4GBPatch(m_session->activeGame().shortName, patcherExePath, output, applyErr)) {
        dialogs::warn(m_parentWindow, "Patch Failed",
            QString("<p>%1</p>"
                    "<p>Patcher output:</p><pre>%2</pre>")
                .arg(applyErr.toHtmlEscaped(), output.toHtmlEscaped()));
        m_statusBar->clearMessage();
        return;
    }

    dialogs::info(m_parentWindow, "Patch Applied",
        QString("<p>FalloutNV.exe has been patched to 4GB.</p>"
                "<p>Patcher output:</p><pre>%1</pre>")
            .arg(output.isEmpty() ? "(no output)" : output.toHtmlEscaped()));

    if (m_patchAction) {
        m_patchAction->setEnabled(false);
        m_patchAction->setToolTip(
            "FalloutNV.exe is already patched to 4GB. Re-running the patcher is unnecessary.");
    }
    m_runButton->setFourGBPatched(true);
    m_statusBar->showMessage("FalloutNV.exe patched to 4GB.", 5000);
}

}
