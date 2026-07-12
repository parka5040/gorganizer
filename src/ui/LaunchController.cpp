#include "LaunchController.h"
#include "GrpcClient.h"
#include "SessionController.h"
#include "RunButtonWidget.h"
#include "Dialogs.h"

#include <QProcess>
#include <QStatusBar>

namespace gorganizer {

LaunchController::LaunchController(AppConfig& config, GrpcClient* grpc,
                                   SessionController* session,
                                   RunButtonWidget* runButton, QStatusBar* statusBar,
                                   QWidget* parentWindow)
    : QObject(parentWindow)
    , m_config(config)
    , m_grpc(grpc)
    , m_session(session)
    , m_runButton(runButton)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    connect(m_grpc, &GrpcClient::gameLaunched, this, &LaunchController::onGameLaunched);
    connect(m_grpc, &GrpcClient::gameLaunchFailed, this, &LaunchController::onGameLaunchFailed);
}

void LaunchController::onRunGame()
{
    if (!m_session->activeGame().detected) {
        dialogs::warn(m_parentWindow, "No Game Selected", "Please select a game first.");
        return;
    }

    auto target = m_runButton->currentTarget();
    if (target.type == RunButtonWidget::TargetInstallTool) {
        if (target.toolId == "ttw-install") {
            emit ttwInstallRequested();
            return;
        }
        if (!m_grpc->isConnected()) {
            dialogs::warn(m_parentWindow, "Not Connected",
                "The daemon must be running to install a script extender.");
            return;
        }
        m_statusBar->showMessage(
            QString("Downloading %1 from Nexus...").arg(target.label));
        QString name, err;
        if (!m_grpc->installScriptExtender(m_session->activeGame().shortName, name, err)) {
            dialogs::warn(m_parentWindow, "Install Failed",
                QString("%1\n\nIf you are a non-premium Nexus user, open the "
                        "mod page in a browser and click 'Download with "
                        "Manager' to trigger an NXM download instead.").arg(err));
            return;
        }
        m_statusBar->showMessage(QString("%1 installed.").arg(name), 5000);
        m_runButton->setGame(m_session->activeGame(),
            m_config.lastToolFor(m_session->activeGame().shortName));
        return;
    }

    if (m_grpc->isConnected()) {
        bool useTool = (target.type == RunButtonWidget::TargetTool);
        m_statusBar->showMessage(
            useTool ? QString("Preparing mods and launching %1...").arg(target.label)
                    : QString("Preparing mods and launching %1...").arg(m_session->activeGame().name));
        m_runButton->setEnabled(false);
        m_grpc->launchGame(m_session->activeGame().shortName, useTool, m_session->currentProfile());
    } else {
        QString steamUrl = QString("steam://rungameid/%1").arg(m_session->activeGame().appId);
        bool launched = QProcess::startDetached("xdg-open", {steamUrl});
        if (!launched) {
            dialogs::warn(m_parentWindow, "Launch Failed",
                "Could not launch Steam. Is Steam installed?");
            return;
        }
        m_statusBar->showMessage("Launched " + m_session->activeGame().name + " (no mods)", 5000);
    }
}

void LaunchController::onTargetChanged(const QString& toolId)
{
    if (m_session->activeGame().detected)
        m_config.setLastToolFor(m_session->activeGame().shortName, toolId);
}

void LaunchController::onGameLaunched(int pid)
{
    m_runButton->setEnabled(true);
    m_statusBar->showMessage(QString("Game launched (PID %1)").arg(pid), 5000);
}

void LaunchController::onGameLaunchFailed(const QString& error)
{
    m_runButton->setEnabled(true);
    if (error.contains("loader_missing:")) {
        const int idx = error.indexOf("loader_missing:");
        const QStringList parts = error.mid(idx + QString("loader_missing:").length())
                                     .split(':', Qt::KeepEmptyParts);
        const QString reason        = parts.value(0);
        const QString configuredExe = parts.value(1);
        const QString installPath   = parts.value(2);

        QString title = "Script extender launch blocked";
        QString body;
        if (reason == "missing") {
            body = QString(
                "Gorganizer can't find the script-extender loader "
                "(<b>%1</b>) in <code>%2</code>.<br><br>"
                "This usually happens after a Steam game update removes or "
                "restores files under the game's install directory. "
                "Reinstall the script extender from <b>Tools &#x2192; Install "
                "script extender</b> to continue."
            ).arg(configuredExe.isEmpty() ? "(none configured)" : configuredExe.toHtmlEscaped(),
                  installPath.toHtmlEscaped());
        } else if (reason == "modified") {
            body = QString(
                "The script-extender files under <code>%1</code> were "
                "modified since they were installed.<br><br>"
                "A Steam game update, manual edit, or anti-cheat tool can "
                "cause this. Reinstall the script extender from "
                "<b>Tools &#x2192; Install script extender</b> so the installed "
                "files match a known-good release."
            ).arg(installPath.toHtmlEscaped());
        } else if (reason == "looks-like-vanilla-launcher") {
            body = QString(
                "The configured loader exe (<b>%1</b>) is larger than any "
                "legitimate script-extender loader &#x2014; it's almost certainly "
                "the vanilla Bethesda launcher a Steam update restored.<br><br>"
                "Reinstall the script extender from <b>Tools &#x2192; Install "
                "script extender</b>, which will replace the file and "
                "re-register the correct launcher."
            ).arg(configuredExe.toHtmlEscaped());
        } else if (reason == "no-loader-configured") {
            body = QString(
                "No script extender is registered for this game yet.<br><br>"
                "Install one from <b>Tools &#x2192; Install script extender</b>, "
                "then try launching with the extender again."
            );
        } else {
            body = QString("Script extender launch failed (%1). Reinstall "
                           "the extender and try again.").arg(reason.toHtmlEscaped());
        }
        dialogs::richWarn(m_parentWindow, title, body);
        m_session->refreshStatusInfo();
        return;
    }

    if (error.contains("fnv4gb_not_applied_for_ttw")) {
        dialogs::warn(m_parentWindow, "Patch FalloutNV.exe to 4GB",
            "<p><b>FalloutNV.exe is not LAA-patched.</b> TTW's merged data "
            "set exceeds FNV's 2&nbsp;GiB memory cap within seconds of the "
            "main menu — that's the \"music plays, then crash\" you just "
            "saw.</p>"
            "<p>Run <b>Tools &#x2192; Patch Fallout to 4GB</b> first, then "
            "try launching again.</p>");
        m_session->refreshStatusInfo();
        return;
    }

    if (error.contains("xnvse_missing_for_ttw")) {
        dialogs::warn(m_parentWindow, "xNVSE Required",
            "<p>TTW launches via <b>nvse_loader.exe</b>, but xNVSE's runtime "
            "DLLs are not installed in the FNV directory.</p>"
            "<p>Open the Run combo and choose <b>Install xNVSE...</b>, then "
            "try launching again.</p>");
        m_session->refreshStatusInfo();
        return;
    }

    dialogs::warn(m_parentWindow, "Launch Failed", error);
    m_session->refreshStatusInfo();
}

}
