#include "LaunchController.h"
#include "GrpcClient.h"
#include "InstallErrorText.h"
#include "ModLoaderController.h"
#include "SessionController.h"
#include "RunButtonWidget.h"
#include "Dialogs.h"

#include <QMessageBox>
#include <QProcess>
#include <QPushButton>
#include <QStatusBar>

namespace gorganizer {

LaunchController::LaunchController(AppConfig& config, GrpcClient* grpc,
                                   SessionController* session,
                                   ModLoaderController* modLoader,
                                   RunButtonWidget* runButton, QStatusBar* statusBar,
                                   QWidget* parentWindow)
    : QObject(parentWindow)
    , m_config(config)
    , m_grpc(grpc)
    , m_session(session)
    , m_modLoader(modLoader)
    , m_runButton(runButton)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    connect(m_grpc, &GrpcClient::gameLaunched, this, &LaunchController::onGameLaunched);
    connect(m_grpc, &GrpcClient::gameLaunchFailed, this, &LaunchController::onGameLaunchFailed);
    connect(m_modLoader, &ModLoaderController::operationActivityChanged, this,
            &LaunchController::onModLoaderActivityChanged);
    connect(m_session, &SessionController::activeGameChanged, this, &LaunchController::onActiveGameChanged);
}

void LaunchController::updateRunEnabled()
{
    const bool loaderBusy = m_modLoader->operationActiveFor(m_session->activeGame().shortName);
    m_runButton->setEnabled(!m_launchPending && !loaderBusy);
}

void LaunchController::onModLoaderActivityChanged(const QString& gameId, bool active)
{
    Q_UNUSED(gameId);
    Q_UNUSED(active);
    updateRunEnabled();
}

void LaunchController::onActiveGameChanged(const GameInfo& game)
{
    Q_UNUSED(game);
    updateRunEnabled();
}

void LaunchController::onRunGame()
{
    if (!m_session->activeGame().detected) {
        dialogs::warn(m_parentWindow, "No Game Selected", "Please select a game first.");
        return;
    }

    if (m_modLoader->operationActiveFor(m_session->activeGame().shortName)) {
        dialogs::info(m_parentWindow, "SMAPI Operation Running",
            "SMAPI is being changed right now. Launch the game when that finishes.");
        return;
    }

    auto target = m_runButton->currentTarget();
    if (target.type == RunButtonWidget::TargetInstallModLoader) {
        m_modLoader->startInstall(m_session->activeGame().shortName);
        return;
    }
    if (target.type == RunButtonWidget::TargetRepairModLoader) {
        m_modLoader->startRepair(m_session->activeGame().shortName);
        return;
    }
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
        m_launchPending = true;
        updateRunEnabled();
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
    const auto type = m_runButton->currentTarget().type;
    if (type == RunButtonWidget::TargetInstallModLoader || type == RunButtonWidget::TargetRepairModLoader)
        return;
    if (m_session->activeGame().detected)
        m_config.setLastToolFor(m_session->activeGame().shortName, toolId);
}

void LaunchController::onGameLaunched(int pid)
{
    m_launchPending = false;
    updateRunEnabled();
    m_statusBar->showMessage(QString("Game launched (PID %1)").arg(pid), 5000);
}

void LaunchController::onGameLaunchFailed(const QString& error)
{
    m_launchPending = false;
    updateRunEnabled();
    const InstallError parsed = parseInstallError(error);
    if (parsed.token == QLatin1String("modloader_unavailable")) {
        const QString gameId = parsed.fields.value(QStringLiteral("game"));
        const QString reason = parsed.fields.value(QStringLiteral("reason"));
        m_modLoader->refreshStatus(gameId);
        if (reason == QLatin1String("unsupported_build"))
            showModLoaderError(m_parentWindow, "Launch Blocked", error);
        else
            offerModLoaderFix(gameId, reason);
        m_session->refreshStatusInfo();
        return;
    }
    if (parsed.token == QLatin1String("modloader_busy")) {
        showModLoaderError(m_parentWindow, "Launch Blocked", error);
        m_session->refreshStatusInfo();
        return;
    }
    if (parsed.token == QLatin1String("game_running") || parsed.token == QLatin1String("daemon_shutting_down")) {
        dialogs::plainWarn(m_parentWindow, "Launch Blocked", daemonErrorMessage(error));
        const QString gameId = parsed.fields.value(QStringLiteral("game"));
        if (!gameId.isEmpty() && m_grpc->isConnected())
            m_grpc->getVfsStatus(gameId);
        m_session->refreshStatusInfo();
        return;
    }
    if (parsed.token == QLatin1String("loader_missing")) {
        const QString reason = parsed.fields.value(QStringLiteral("reason"));
        const QString configuredExe = parsed.fields.value(QStringLiteral("exe"));
        const QString installPath = parsed.fields.value(QStringLiteral("install_path"));

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

    if (parsed.token == QLatin1String("fnv4gb_not_applied_for_ttw")) {
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

    if (parsed.token == QLatin1String("xnvse_missing_for_ttw")) {
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

void LaunchController::offerModLoaderFix(const QString& gameId, const QString& reason)
{
    const QString title = QStringLiteral("SMAPI Required");
    const bool install = reason == QLatin1String("not_installed");
    QMessageBox box(m_parentWindow);
    box.setWindowTitle(title);
    box.setIcon(QMessageBox::Warning);
    box.setTextFormat(Qt::PlainText);
    box.setText(QStringLiteral("SMAPI is %1. Your enabled mods need it.").arg(modLoaderUnavailableReasonText(reason)));
    box.setInformativeText(install
        ? QStringLiteral("Installing downloads the newest stable SMAPI release from GitHub, checks its published "
                         "SHA-256 checksum, and applies it to the game folder.")
        : QStringLiteral("Repairing runs the SMAPI installer gorganizer kept from the last install or update "
                         "again and applies the result to the game folder; nothing is downloaded."));
    QPushButton* fixBtn = box.addButton(install ? QStringLiteral("Install SMAPI") : QStringLiteral("Repair SMAPI"),
                                        QMessageBox::AcceptRole);
    box.addButton(QMessageBox::Cancel);
    box.setDefaultButton(fixBtn);
    box.exec();
    if (box.clickedButton() != fixBtn)
        return;
    if (install)
        m_modLoader->startInstall(gameId, true);
    else
        m_modLoader->startRepair(gameId, true);
}

}
