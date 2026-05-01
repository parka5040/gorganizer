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

// Force Fusion style for consistent QSS rendering across DEs.
static QStyle* makeFusionStyle()
{
    return QStyleFactory::create("Fusion");
}

// Locate gorganizerd next to the frontend, in the dev layout, or in PATH.
static QString findDaemonBinary()
{
    QString appDir = QCoreApplication::applicationDirPath();
    QString candidate = appDir + "/gorganizerd";
    if (QFileInfo::exists(candidate))
        return candidate;

    candidate = appDir + "/../../gorganizerd";
    if (QFileInfo::exists(candidate))
        return QFileInfo(candidate).canonicalFilePath();

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

    qint64 daemonPid = 0;
    bool daemonOwned = false;

    QString sock = socketPath();

    bool alreadyRunning = QFileInfo::exists(sock);

    if (!alreadyRunning) {
        QString daemonBin = findDaemonBinary();
        if (daemonBin.isEmpty()) {
            qWarning("gorganizerd not found — running without daemon");
        } else {
            QFile::remove(sock);

            daemonOwned = QProcess::startDetached(
                daemonBin, {"--log-level", "info"},
                QString(), &daemonPid);

            if (!daemonOwned) {
                qWarning("Failed to start gorganizerd");
            } else {
                for (int i = 0; i < 30; ++i) {
                    if (QFileInfo::exists(sock))
                        break;
                    QThread::msleep(100);
                }
            }
        }
    }

    gorganizer::GrpcClient grpcClient;
    grpcClient.connectToDaemon();

    if (!wizardApiKey.isEmpty()) {
        QObject::connect(&grpcClient, &gorganizer::GrpcClient::connected, &grpcClient,
            [&grpcClient, wizardApiKey] { grpcClient.setNexusAPIKey(wizardApiKey); },
            Qt::SingleShotConnection);
    }

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

    for (int i = 1; i < argc; ++i) {
        QString arg = QString::fromUtf8(argv[i]);
        if (arg.startsWith("nxm://")) {
            grpcClient.startDownload(arg);
            break;
        }
    }

    int exitCode = app.exec();

    if (daemonOwned) {
        QString shutdownErr;
        if (!grpcClient.shutdownDaemonSync(3000, 10000, shutdownErr)) {
            qWarning("daemon shutdown not confirmed: %s — relying on shell wrapper to reap it",
                     qUtf8Printable(shutdownErr));
        }
    }

    return exitCode;
}
