#include "UpdateController.h"
#include "AppConfig.h"
#include "ErrorPresenter.h"
#include "GrpcClient.h"
#include "SafeLinks.h"

#include <QClipboard>
#include <QCoreApplication>
#include <QDir>
#include <QGuiApplication>
#include <QProcessEnvironment>
#include <QRegularExpression>
#include <QTimer>
#include <QWidget>
#include <signal.h>
#include <sys/prctl.h>
#include <unistd.h>
#include <utility>

namespace gorganizer {

namespace {

QString releaseBase(const QString& version)
{
    static const QRegularExpression pattern(
        QStringLiteral("\\A([0-9]{1,9}\\.[0-9]{1,9}\\.[0-9]{1,9})(\\+[0-9A-Za-z.\\-]{1,64})?\\z"));
    const auto match = pattern.match(version);
    return match.hasMatch() ? match.captured(1) : QString();
}

}

UpdateController::UpdateController(GrpcClient* grpc, AppConfig& config, NoticeBar* bar,
                                   QWidget* parentWindow, bool daemonStopsOnExit)
    : QObject(parentWindow)
    , m_grpc(grpc)
    , m_config(config)
    , m_bar(bar)
    , m_parentWindow(parentWindow)
    , m_daemonStopsOnExit(daemonStopsOnExit)
{
    m_connectedHandler = connect(m_grpc, &GrpcClient::connected, this, &UpdateController::onConnected);
    connect(m_grpc, &GrpcClient::updateCheckFinished, this, &UpdateController::onCheckFinished);
    connect(m_grpc, &GrpcClient::updateCheckFailed, this, &UpdateController::onCheckFailed);
    connect(m_grpc, &GrpcClient::workersStopped, this, [this] {
        if (!m_checkId)
            return;
        m_checkId = 0;
        const bool interactive = std::exchange(m_interactive, false);
        if (interactive)
            showCheckNotice(NoticeBar::Kind::Warning,
                QStringLiteral("Unable to check for updates right now. Please try again later."), {}, true);
    });
    connect(m_bar, &NoticeBar::dismissed, this, &UpdateController::onDismissed);
}

void UpdateController::start()
{
    if (m_started)
        return;
    m_started = true;
    m_layout = detectInstallLayout();
    if (m_layout.kind == InstallKind::Development) {
        releaseConnection();
        return;
    }
    QString installed;
    if (m_layout.kind == InstallKind::Prebuilt
        && readInstalledVersion(m_layout.releasesRoot, &installed)
        && installed != m_layout.runningBase) {
        m_installedDifferent = installed;
        m_skipAutomatic = true;
        showInstalled(installed);
        releaseConnection();
        return;
    }
    if (m_grpc->isConnected())
        onConnected();
    if (m_skipAutomatic)
        return;
    const auto preference = m_config.updateCheckAtStartup();
    if (!preference)
        showConsent();
    else if (*preference) {
        m_autoQueued = true;
        sendCheck();
    }
}

void UpdateController::onConnected()
{
    if (!m_started || m_layout.kind == InstallKind::Development)
        return;
    if (!m_installedDifferent.isEmpty()) {
        sendCheck();
        if (!m_autoQueued)
            releaseConnection();
        return;
    }
    if (!m_readinessChecked) {
        m_readinessChecked = true;
        GrpcReadiness readiness;
        GrpcError error;
        const QString daemonBase = m_grpc->health(readiness, error)
            ? releaseBase(readiness.version) : QString();
        if (!daemonBase.isEmpty() && compareVersions(daemonBase, m_layout.runningBase) < 0) {
            m_olderDaemon = daemonBase;
            m_skipAutomatic = true;
            if (!m_interactive)
                m_autoQueued = false;
            if (!m_checkId && !m_interactive && m_notice != Notice::Updating
                && m_notice != Notice::UpdateResult) {
                m_notice = Notice::OlderDaemon;
                m_bar->showNotice(NoticeBar::Kind::Warning,
                    QStringLiteral("Gorganizer's background service is still running version %1. "
                                   "Restart your computer to finish updating.").arg(daemonBase), {}, true);
            }
        }
    }
    sendCheck();
    if (!m_autoQueued)
        releaseConnection();
}

void UpdateController::showConsent()
{
    if (m_notice == Notice::Updating || m_notice == Notice::UpdateResult)
        return;
    m_notice = Notice::Consent;
    m_bar->showNotice(NoticeBar::Kind::Info,
        QStringLiteral("Check for Gorganizer updates each time it starts? This contacts GitHub; "
                       "no game or mod information is sent."),
        {{QStringLiteral("Check at Startup"), [this] {
            m_config.setUpdateCheckAtStartup(true);
            m_bar->clear();
            m_notice = Notice::None;
            m_interactive = true;
            m_autoQueued = true;
            sendCheck();
        }}, {QStringLiteral("Don't Check"), [this] {
            m_config.setUpdateCheckAtStartup(false);
            m_bar->clear();
            m_notice = Notice::None;
        }}}, true);
}

void UpdateController::showInstalled(const QString& version)
{
    if (m_notice == Notice::Updating || m_notice == Notice::UpdateResult)
        return;
    m_notice = Notice::Installed;
    m_bar->showNotice(NoticeBar::Kind::Info,
        QStringLiteral("Gorganizer %1 is installed. Close and reopen Gorganizer to use it.").arg(version),
        {{QStringLiteral("Quit Gorganizer"), [this] { m_parentWindow->close(); }}}, true);
}

void UpdateController::sendCheck()
{
    if (!m_interactive && m_config.updateCheckAtStartup() != std::optional<bool>(true))
        m_autoQueued = false;
    if (!m_autoQueued || m_checkId || (!m_interactive && (m_skipAutomatic || !m_readinessChecked))
        || updating() || m_notice == Notice::UpdateResult)
        return;
    if (!m_grpc->isConnected()) {
        if (!m_connectedHandler)
            m_connectedHandler = connect(m_grpc, &GrpcClient::connected, this, &UpdateController::onConnected);
        return;
    }
    m_autoQueued = false;
    m_discardAutomatic = false;
    m_checkId = m_grpc->checkForUpdate(m_layout.runningVersion);
    releaseConnection();
}

void UpdateController::releaseConnection()
{
    if (m_connectedHandler) {
        disconnect(m_connectedHandler);
        m_connectedHandler = {};
    }
}

void UpdateController::checkNow()
{
    if (updating() || m_notice == Notice::UpdateResult)
        return;
    if (!m_started) {
        m_layout = detectInstallLayout();
        m_started = true;
    }
    if (m_layout.kind == InstallKind::Development) {
        showCheckNotice(NoticeBar::Kind::Info,
            QStringLiteral("Update checks are not available for development builds."), {}, true);
        return;
    }
    if (m_checkId || m_autoQueued) {
        m_interactive = true;
        m_discardAutomatic = false;
        sendCheck();
        return;
    }
    if (!m_grpc->isConnected()) {
        showCheckNotice(NoticeBar::Kind::Warning,
            QStringLiteral("Unable to check for updates right now. Please try again later."), {}, true);
        return;
    }
    m_interactive = true;
    m_autoQueued = true;
    sendCheck();
}

void UpdateController::preferenceChanged()
{
    const auto preference = m_config.updateCheckAtStartup();
    if (preference && !*preference) {
        if (!m_interactive) {
            m_autoQueued = false;
            m_discardAutomatic = true;
            if (m_notice == Notice::CheckResult && !m_resultInteractive) {
                m_bar->clear();
                m_notice = Notice::None;
            }
        }
        if (m_notice == Notice::Consent) {
            m_bar->clear();
            m_notice = Notice::None;
        }
    } else if (preference && m_notice == Notice::Consent) {
        m_bar->clear();
        m_notice = Notice::None;
    }
}

void UpdateController::onCheckFinished(quint64 requestId, const GrpcUpdateCheck& result)
{
    if (requestId != m_checkId || !requestId)
        return;
    m_checkId = 0;
    const bool interactive = std::exchange(m_interactive, false);
    if (updating() || m_notice == Notice::UpdateResult
        || (!interactive && (m_discardAutomatic || m_autoDismissed
                             || m_config.updateCheckAtStartup() != std::optional<bool>(true))))
        return;
    showResult(result, interactive);
}

void UpdateController::onCheckFailed(quint64 requestId, const QString& error, int grpcCode)
{
    Q_UNUSED(error);
    if (requestId != m_checkId || !requestId)
        return;
    m_checkId = 0;
    const bool interactive = std::exchange(m_interactive, false);
    if (updating() || m_notice == Notice::UpdateResult
        || (!interactive && (m_discardAutomatic || m_autoDismissed
                             || m_config.updateCheckAtStartup() != std::optional<bool>(true))))
        return;
    if (grpcCode == GrpcStatusUnimplemented) {
        if (interactive)
            showCheckNotice(NoticeBar::Kind::Warning,
                QStringLiteral("Gorganizer's background service is from an older version and cannot check "
                               "for updates. Restart your computer, then try again."), {}, true);
        return;
    }
    showCheckNotice(NoticeBar::Kind::Warning,
        QStringLiteral("Unable to check for updates right now. Please try again later."), {}, interactive);
}

void UpdateController::showResult(const GrpcUpdateCheck& result, bool interactive)
{
    using Outcome = GrpcUpdateCheck::Outcome;
    switch (result.outcome) {
    case Outcome::Offline:
        showCheckNotice(NoticeBar::Kind::Warning,
            QStringLiteral("Unable to check for updates. Please check your Internet connection"), {}, interactive);
        return;
    case Outcome::Unspecified:
    case Outcome::Unavailable:
        showCheckNotice(NoticeBar::Kind::Warning,
            QStringLiteral("Unable to check for updates right now. Please try again later."), {}, interactive);
        return;
    case Outcome::NotSupported:
        if (interactive)
            showCheckNotice(NoticeBar::Kind::Info,
                QStringLiteral("Update checks are not available for development builds."), {}, true);
        return;
    case Outcome::UpToDate:
        if (interactive)
            showCheckNotice(NoticeBar::Kind::Info,
                QStringLiteral("Gorganizer %1 is up to date.").arg(m_layout.runningBase), {}, true);
        return;
    case Outcome::UpdateAvailable:
        break;
    }

    const QString latest = result.latestVersion;
    const QString running = m_layout.runningBase;
    if (releaseBase(latest) != latest) {
        showCheckNotice(NoticeBar::Kind::Warning,
            QStringLiteral("Unable to check for updates right now. Please try again later."), {}, interactive);
        return;
    }
    if (compareVersions(latest, running) <= 0) {
        if (interactive)
            showCheckNotice(NoticeBar::Kind::Info,
                QStringLiteral("Gorganizer %1 is up to date.").arg(running), {}, true);
        return;
    }
    const NoticeBar::Action notes{QStringLiteral("What's New"), [this, url = result.notesUrl] {
        openWebLink(m_parentWindow, url);
    }};
    if (m_layout.kind == InstallKind::Prebuilt) {
        QString installed;
        if (!readInstalledVersion(m_layout.releasesRoot, &installed)) {
            showCheckNotice(NoticeBar::Kind::Info,
                QStringLiteral("Gorganizer %1 is available, but this installation could not be checked. "
                               "Use What's New to update manually.").arg(latest), {notes}, interactive);
        } else if (installed == latest) {
            showCheckNotice(NoticeBar::Kind::Info,
                QStringLiteral("Gorganizer %1 is already installed. Close and reopen Gorganizer to use it.").arg(latest),
                {{QStringLiteral("Quit Gorganizer"), [this] { m_parentWindow->close(); }}}, interactive);
        } else if (compareVersions(installed, latest) > 0) {
            showCheckNotice(NoticeBar::Kind::Info,
                QStringLiteral("Gorganizer %1 is installed. Close and reopen Gorganizer to use it.").arg(installed),
                {{QStringLiteral("Quit Gorganizer"), [this] { m_parentWindow->close(); }}}, interactive);
        } else {
            showCheckNotice(NoticeBar::Kind::Info,
                QStringLiteral("Gorganizer %1 is available. You have %2.").arg(latest, running),
                {{QStringLiteral("Update Now"), [this, latest, bundle = m_layout.bundleDir] {
                    updateNow(latest, bundle);
                }}, notes}, interactive);
        }
        return;
    }
    if (m_layout.kind == InstallKind::Source) {
        QList<NoticeBar::Action> actions;
        const QString command = shellQuote(m_layout.scriptPath);
        if (!command.isEmpty()) {
            actions.append({QStringLiteral("Copy Command"), [this, command] {
                QGuiApplication::clipboard()->setText(command + QStringLiteral(" update"));
                m_bar->renameAction(QStringLiteral("Copy Command"), QStringLiteral("Copied"));
                QTimer::singleShot(1600, this, [this] {
                    m_bar->renameAction(QStringLiteral("Copied"), QStringLiteral("Copy Command"));
                });
            }});
        }
        actions.append(notes);
        showCheckNotice(NoticeBar::Kind::Info,
            QStringLiteral("Gorganizer %1 has been released. This source build (%2) updates from its Git "
                           "branch with its update command.").arg(latest, running), actions, interactive);
        return;
    }
    showCheckNotice(NoticeBar::Kind::Info,
        QStringLiteral("Gorganizer %1 is available. You have %2.").arg(latest, running), {notes}, interactive);
}

void UpdateController::showCheckNotice(NoticeBar::Kind kind, const QString& text,
                                        const QList<NoticeBar::Action>& actions, bool interactive)
{
    if (updating() || m_notice == Notice::UpdateResult)
        return;
    if (!interactive && m_autoDismissed)
        return;
    m_resultInteractive = interactive;
    m_notice = Notice::CheckResult;
    m_bar->showNotice(kind, text, actions, true);
}

void UpdateController::restoreNotice()
{
    if (m_layout.kind == InstallKind::Development)
        return;
    if (!m_config.updateCheckAtStartup())
        showConsent();
    else if (m_autoDismissed)
        return;
    else if (!m_installedDifferent.isEmpty())
        showInstalled(m_installedDifferent);
    else if (!m_olderDaemon.isEmpty()) {
        m_notice = Notice::OlderDaemon;
        m_bar->showNotice(NoticeBar::Kind::Warning,
            QStringLiteral("Gorganizer's background service is still running version %1. "
                           "Restart your computer to finish updating.").arg(m_olderDaemon), {}, true);
    }
}

void UpdateController::onDismissed()
{
    const Notice previous = std::exchange(m_notice, Notice::None);
    if (previous == Notice::CheckResult && m_resultInteractive) {
        restoreNotice();
        return;
    }
    m_autoDismissed = true;
}

void UpdateController::setDaemonStopsOnExit(bool stops)
{
    m_daemonStopsOnExit = stops;
}

bool UpdateController::updating() const
{
    return !m_process.isNull();
}

QString UpdateController::updateCloseParagraph() const
{
    return QStringLiteral("Gorganizer is installing an update. Quitting stops it; the update either finishes "
                          "or leaves your current version unchanged.");
}

void UpdateController::updateNow(const QString& version, const QString& bundleDir)
{
    if (updating() || m_notice == Notice::UpdateResult)
        return;
    const AppInstallLayout current = detectInstallLayout();
    QString selected;
    if (current.kind != InstallKind::Prebuilt || current.bundleDir != bundleDir
        || !QDir::isAbsolutePath(current.dataHome)
        || releaseBase(version) != version
        || !readInstalledVersion(current.releasesRoot, &selected)
        || compareVersions(selected, version) >= 0) {
        showCheckNotice(NoticeBar::Kind::Warning,
            QStringLiteral("The update could not start because this installation could not be checked."), {}, true);
        return;
    }

    auto* process = new QProcess(this);
    process->setProgram(current.scriptPath);
    process->setArguments({QStringLiteral("update"), QStringLiteral("--tag"), QLatin1Char('v') + version});
    process->setStandardInputFile(QProcess::nullDevice());
    process->setProcessChannelMode(QProcess::MergedChannels);
    QProcessEnvironment env = QProcessEnvironment::systemEnvironment();
    env.insert(QStringLiteral("GIT_TERMINAL_PROMPT"), QStringLiteral("0"));
    env.insert(QStringLiteral("XDG_DATA_HOME"), current.dataHome);
    process->setProcessEnvironment(env);
    const pid_t parent = getpid();
    process->setChildProcessModifier([parent] {
        if (prctl(PR_SET_PDEATHSIG, SIGTERM) != 0)
            _exit(127);
        if (getppid() != parent)
            _exit(127);
    });
    m_process = process;
    m_target = version;
    m_selectedBefore = selected;
    m_releasesRoot = current.releasesRoot;
    m_output.clear();
    m_cancelRequested = false;
    m_notice = Notice::Updating;
    m_bar->showNotice(NoticeBar::Kind::Info,
        QStringLiteral("Updating Gorganizer to %1…").arg(version),
        {{QStringLiteral("Cancel"), [this] { cancelUpdate(); }}}, false);
    emit updatingChanged(true);
    connect(process, &QProcess::readyRead, this, [this, process] { collectOutput(process); });
    connect(process, &QProcess::finished, this,
            [this, process](int code, QProcess::ExitStatus status) { reconcile(process, code, status); });
    connect(process, &QProcess::errorOccurred, this, [this, process](QProcess::ProcessError error) {
        if (error == QProcess::FailedToStart)
            reconcile(process, -1, QProcess::CrashExit);
    });
    process->start();
}

void UpdateController::cancelUpdate()
{
    if (!m_process || m_cancelRequested)
        return;
    m_cancelRequested = true;
    QProcess* process = m_process;
    process->terminate();
    auto* timer = new QTimer(process);
    timer->setSingleShot(true);
    m_killTimer = timer;
    connect(timer, &QTimer::timeout, process, [process] {
        if (process->state() != QProcess::NotRunning)
            process->kill();
    });
    timer->start(15000);
    m_bar->showNotice(NoticeBar::Kind::Info, QStringLiteral("Stopping the update…"), {}, false);
}

void UpdateController::stopUpdateThen(std::function<void()> done)
{
    if (!updating()) {
        if (done) done();
        return;
    }
    if (m_closeDone)
        return;
    m_closeDone = std::move(done);
    cancelUpdate();
    QProcess* process = m_process;
    QTimer::singleShot(20000, this, [this, process] {
        if (m_process == process && m_closeDone) {
            auto callback = std::move(m_closeDone);
            callback();
        }
    });
}

void UpdateController::collectOutput(QProcess* process)
{
    if (m_process != process)
        return;
    m_output += process->readAll();
    constexpr qsizetype limit = 256 * 1024;
    if (m_output.size() > limit)
        m_output.remove(0, m_output.size() - limit);
}

void UpdateController::reconcile(QProcess* process, int exitCode, QProcess::ExitStatus exitStatus)
{
    if (m_process != process)
        return;
    collectOutput(process);
    if (m_killTimer)
        m_killTimer->stop();
    m_killTimer = nullptr;
    const QString version = m_target;
    const QString before = m_selectedBefore;
    QString raw = QString::fromUtf8(m_output);
    const QString exitLine = QStringLiteral("Exit status: %1; exit code: %2")
        .arg(exitStatus == QProcess::NormalExit ? QStringLiteral("NormalExit") : QStringLiteral("CrashExit"))
        .arg(exitCode);
    if (raw.isEmpty() && process->error() != QProcess::UnknownError)
        raw = process->errorString();
    if (!raw.isEmpty())
        raw += QLatin1Char('\n');
    raw += exitLine;
    const bool succeeded = exitStatus == QProcess::NormalExit && exitCode == 0;
    const bool cancelled = m_cancelRequested;
    m_process = nullptr;
    process->deleteLater();
    emit updatingChanged(false);

    QString selected;
    const bool known = readInstalledVersion(m_releasesRoot, &selected);
    QList<NoticeBar::Action> actions;
    NoticeBar::Kind kind = NoticeBar::Kind::Info;
    QString text;
    const NoticeBar::Action details{QStringLiteral("Details"), [this, raw] { showDetails(raw); }};
    const NoticeBar::Action quit{QStringLiteral("Quit Gorganizer"), [this] { m_parentWindow->close(); }};
    if (known && selected == version) {
        if (succeeded) {
            text = m_daemonStopsOnExit
                ? QStringLiteral("Gorganizer %1 is installed. Close and reopen Gorganizer to use it.").arg(version)
                : QStringLiteral("Gorganizer %1 is installed. Quit Gorganizer, then restart your computer "
                                 "to finish updating.").arg(version);
            actions.append(quit);
        } else {
            kind = NoticeBar::Kind::Warning;
            text = QStringLiteral("Gorganizer %1 is installed, but the update reported a problem.").arg(version);
            actions = {details, quit};
        }
    } else if (known && selected == before && before != version) {
        if (cancelled) {
            text = QStringLiteral("The update was cancelled. Gorganizer %1 is still selected.").arg(before);
        } else if (!succeeded) {
            kind = NoticeBar::Kind::Warning;
            text = QStringLiteral("The update did not finish. Gorganizer %1 is still selected.").arg(before);
            actions.append(details);
        } else {
            kind = NoticeBar::Kind::Warning;
            text = QStringLiteral("The update ended unexpectedly; Gorganizer %1 is still selected.").arg(before);
            actions.append(details);
        }
    } else if (known) {
        kind = NoticeBar::Kind::Warning;
        text = QStringLiteral("Gorganizer %1 is now selected; the update's outcome is uncertain.").arg(selected);
        actions.append(details);
    } else {
        kind = NoticeBar::Kind::Warning;
        text = QStringLiteral("The update's outcome could not be verified.");
        actions.append(details);
    }
    m_notice = Notice::UpdateResult;
    m_bar->showNotice(kind, text, actions, true);
    if (m_closeDone) {
        auto callback = std::move(m_closeDone);
        callback();
    }
}

void UpdateController::showDetails(const QString& raw)
{
    presentError(m_parentWindow, QStringLiteral("Update Details"), QStringLiteral("update Gorganizer"), raw);
}

}
