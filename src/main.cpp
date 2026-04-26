#include <QApplication>
#include <QStyleFactory>
#include <QStyleHints>
#include <QProcess>
#include <QFileInfo>
#include <QDir>
#include <QThread>
#include <QStandardPaths>
#include "AppConfig.h"
#include "SetupWizard.h"
#include "MainWindow.h"
#include "GrpcClient.h"
#include "SplashScreen.h"
#include "ThemeManager.h"
#include <QEventLoop>
#include <QMessageBox>
#include <cstdio>
#include <cstring>

#ifndef GORGANIZER_VERSION
#define GORGANIZER_VERSION "dev"
#endif

// Always render with Qt's Fusion style. Fusion ships with qtbase6 on every
// distro and renders identically across DEs/X11/Wayland; the QSS theme files
// in resources/themes/ were authored against it. Honoring the user's
// QT_STYLE_OVERRIDE or qt6ct/qt6gtk2 globals produced inconsistent rendering
// (e.g., GTK-bridged styles ignoring our QSS) on Artix and other distros.
static QStyle* makeFusionStyle()
{
    return QStyleFactory::create("Fusion");
}

// Find the gorganizerd binary: same directory as the frontend, or in PATH.
static QString findDaemonBinary()
{
    // 1. Next to the frontend binary (dev and installed layout).
    QString appDir = QCoreApplication::applicationDirPath();
    QString candidate = appDir + "/gorganizerd";
    if (QFileInfo::exists(candidate))
        return candidate;

    // 2. One level up from build/src/ (dev layout: binary is build/src/gorganizer,
    //    daemon is at project root as ./gorganizerd).
    candidate = appDir + "/../../gorganizerd";
    if (QFileInfo::exists(candidate))
        return QFileInfo(candidate).canonicalFilePath();

    // 3. In PATH.
    QString inPath = QStandardPaths::findExecutable("gorganizerd");
    if (!inPath.isEmpty())
        return inPath;

    return {};
}

static QString socketPath()
{
    const char* xdg = std::getenv("XDG_RUNTIME_DIR");
    QString dir = xdg ? QString::fromUtf8(xdg) : QDir::tempPath();
    return dir + "/gorganizer/gorganizer.sock";
}

int main(int argc, char* argv[])
{
    // Handle --version before constructing QApplication so the binary can be
    // invoked from package scripts / CI smoke tests on systems with no
    // display.
    for (int i = 1; i < argc; ++i) {
        if (std::strcmp(argv[i], "--version") == 0 || std::strcmp(argv[i], "-v") == 0) {
            std::printf("gorganizer-gui %s\n", GORGANIZER_VERSION);
            return 0;
        }
    }

    qunsetenv("QT_STYLE_OVERRIDE");

    QApplication app(argc, argv);
    app.setApplicationName("gorganizer");
    app.setOrganizationName("gorganizer");
    app.setApplicationVersion(GORGANIZER_VERSION);

    if (QStyle* fusion = makeFusionStyle())
        app.setStyle(fusion);
    else
        qWarning("gorganizer: Fusion style unavailable — Qt6 base install may be incomplete");

    gorganizer::AppConfig config;
    gorganizer::ThemeManager::applyMode(config.appearanceMode(), config.preferredStyle());

    // When the user is on "System" mode, repaint as the OS toggles between
    // light and dark. The lambda re-reads config so a mid-session mode change
    // doesn't keep us subscribed to a now-irrelevant signal.
    QObject::connect(QGuiApplication::styleHints(),
                     &QStyleHints::colorSchemeChanged, &app,
                     [&config](Qt::ColorScheme) {
                         if (config.appearanceMode() == "system")
                             gorganizer::ThemeManager::applyMode(
                                 "system", config.preferredStyle());
                     });

    QString wizardApiKey;
    if (config.isFirstBoot()) {
        gorganizer::SetupWizard wizard(config);
        if (wizard.exec() == QDialog::Rejected)
            return 0;
        wizardApiKey = wizard.validatedApiKey();
    }

    // --- Spawn the daemon as a detached child process ---
    //
    // startDetached (rather than the QProcess instance form) matters: when
    // the user closes the GUI while a game is still running, the daemon
    // must outlive the GUI long enough to unmount the FUSE Data/ overlay
    // only AFTER the game exits. A non-detached QProcess would SIGKILL
    // the daemon on ~QProcess(), which rips the mount out mid-game and
    // the user sees "splash plays, menu stalls". The daemon's own
    // shutdown RPC blocks on in-flight launches, so we can safely ask it
    // to shut down and then just exit the GUI — the daemon cleans up on
    // its own schedule.
    qint64 daemonPid = 0;
    bool daemonOwned = false;

    QString sock = socketPath();

    // Check if a daemon is already running (socket exists and is connectable).
    bool alreadyRunning = QFileInfo::exists(sock);

    if (!alreadyRunning) {
        QString daemonBin = findDaemonBinary();
        if (daemonBin.isEmpty()) {
            qWarning("gorganizerd not found — running without daemon");
        } else {
            // Clean stale socket.
            QFile::remove(sock);

            daemonOwned = QProcess::startDetached(
                daemonBin, {"--log-level", "info"},
                QString(), &daemonPid);

            if (!daemonOwned) {
                qWarning("Failed to start gorganizerd");
            } else {
                // Wait for socket to appear.
                for (int i = 0; i < 30; ++i) {
                    if (QFileInfo::exists(sock))
                        break;
                    QThread::msleep(100);
                }
            }
        }
    }

    // --- Connect and run ---
    gorganizer::GrpcClient grpcClient;
    grpcClient.connectToDaemon();

    // If the SetupWizard captured an API key, hand it to the running daemon.
    // The wizard wrote it to config.json, but the daemon already loaded its
    // config at startup — without this RPC its downloadMgr stays nil and
    // the user has to dig into Tools → Settings to make NXM work after
    // first install.
    if (!wizardApiKey.isEmpty()) {
        QObject::connect(&grpcClient, &gorganizer::GrpcClient::connected, &grpcClient,
            [&grpcClient, wizardApiKey] { grpcClient.setNexusAPIKey(wizardApiKey); },
            Qt::SingleShotConnection);
    }

    // Show the splash before constructing MainWindow so the user sees
    // *something* immediately, instead of an empty window staring back at
    // them while the daemon scans Steam libraries and warms its caches.
    // The splash polls Health() and self-completes when the daemon
    // reports games_warmed=true.
    {
        gorganizer::SplashScreen splash(&grpcClient);
        splash.show();
        QEventLoop loop;
        bool ok = false;
        QString lastStepSeen;
        QObject::connect(&splash, &gorganizer::SplashScreen::ready, &loop, [&]() {
            ok = true;
            loop.quit();
        });
        QObject::connect(&splash, &gorganizer::SplashScreen::failed, &loop,
            [&](const QString& lastStep) {
                ok = false;
                lastStepSeen = lastStep;
                loop.quit();
            });
        // Hard cap on the splash. Without this, a daemon that hangs in
        // warmup (stuck Steam scan, blocked filesystem) freezes the
        // GUI on the splash forever — user can't even close the
        // window because the event loop only quits on
        // ready/failed. 30s is enough for any realistic warmup; on
        // expiry we fall through into the same failure path the
        // splash itself would have taken.
        QTimer watchdog;
        watchdog.setSingleShot(true);
        QObject::connect(&watchdog, &QTimer::timeout, &loop, [&]() {
            if (loop.isRunning()) {
                ok = false;
                lastStepSeen = QStringLiteral("(splash watchdog timeout)");
                loop.quit();
            }
        });
        watchdog.start(30000);

        splash.startPolling();
        loop.exec();
        watchdog.stop();
        splash.close();
        if (!ok) {
            QMessageBox::warning(nullptr, "Daemon startup timed out",
                QString("The Gorganizer daemon did not finish initializing in time.\n\n"
                        "Last step seen: %1\n\n"
                        "Check the daemon log for details:\n"
                        "  $XDG_STATE_HOME/gorganizer/gorganizerd.log\n"
                        "  (or ~/.local/state/gorganizer/gorganizerd.log)").arg(lastStepSeen));
            return 1;
        }
    }

    gorganizer::MainWindow mainWindow(config, &grpcClient);
    mainWindow.show();

    // Forward any NXM URI passed as a command-line argument.
    for (int i = 1; i < argc; ++i) {
        QString arg = QString::fromUtf8(argv[i]);
        if (arg.startsWith("nxm://")) {
            // Daemon is already warm by the time we get here, so forward
            // immediately rather than waiting on connected() again.
            grpcClient.startDownload(arg);
            break;
        }
    }

    int exitCode = app.exec();

    // --- Ask the daemon to shut down ---
    //
    // Synchronous with a bounded deadline + a socket-disappearance
    // poll. The daemon is detached, so we don't reap its PID — but we
    // do want to know it's actually gone before the GUI exits, so the
    // shell wrapper's `kill_stale_daemons` doesn't race the next
    // launch and the user doesn't see a stale socket warning.
    //
    // We intentionally do NOT terminate()/kill() the daemon here —
    // doing so while a game is running would tear the VFS mount out
    // mid-play. The daemon's own Shutdown handler blocks on launched
    // games up to its internal 30s timeout (see daemon.shutdownTimeout).
    if (daemonOwned) {
        QString shutdownErr;
        // 3s for the RPC ack, then up to 10s for the socket file to
        // disappear (covers the daemon's deactivate + IPC.Stop path).
        if (!grpcClient.shutdownDaemonSync(3000, 10000, shutdownErr)) {
            qWarning("daemon shutdown not confirmed: %s — relying on shell wrapper to reap it",
                     qUtf8Printable(shutdownErr));
        }
    }

    return exitCode;
}
