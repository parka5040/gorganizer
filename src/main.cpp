#include <QApplication>
#include <QStyleFactory>
#include <QStyleHints>
#include <QtGlobal>
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
#include <vector>
#include <QByteArray>
#include <unistd.h>

#ifndef GORGANIZER_VERSION
#define GORGANIZER_VERSION "dev"
#endif

// makeFusionStyle returns Qt's Fusion style.
static QStyle* makeFusionStyle()
{
    return QStyleFactory::create("Fusion");
}

// findDaemonBinary locates the daemon executable next to the GUI or on PATH.
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

// findCtlBinary locates the supervisor beside the GUI or in the development layout.
static QString findCtlBinary()
{
    QString appDir = QCoreApplication::applicationDirPath();
    for (const QString& path : {appDir + "/gorganizerctl", appDir + "/../../gorganizerctl"}) {
        QFileInfo info(path);
        if (info.isFile() && info.isExecutable())
            return info.canonicalFilePath();
    }
    return {};
}

// socketPath returns the daemon socket path for the current GUI session.
static QString socketPath()
{
    if (qgetenv("GORGANIZER_SUPERVISED") == "1") {
        QByteArray configured = qgetenv("GORGANIZER_SOCKET");
        if (!configured.isEmpty())
            return QString::fromUtf8(configured);
    }
    const char* xdg = std::getenv("XDG_RUNTIME_DIR");
    if (xdg && xdg[0])
        return QString::fromUtf8(xdg) + "/gorganizer/gorganizer.sock";
    return QDir::tempPath() + "/gorganizer-" + QString::number(getuid()) + "/gorganizer.sock";
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
    for (int i = 1; i < argc; ++i) {
        QString arg = QString::fromUtf8(argv[i]);
        if (arg.startsWith("nxm://")) {
            QString ctl = findCtlBinary();
            if (ctl.isEmpty() || !QProcess::startDetached(ctl, {QStringLiteral("nxm"), arg}))
                qWarning("Gorganizer could not add this download. Open it from the menu and try again.");
            return 0;
        }
    }
    bool supervised = qgetenv("GORGANIZER_SUPERVISED") == "1";
    if (!supervised) {
        QString ctl = findCtlBinary();
        if (!ctl.isEmpty()) {
            QByteArray binary = QFile::encodeName(ctl);
            QByteArray gui = QFile::encodeName(QCoreApplication::applicationFilePath());
            std::vector<char*> args = {binary.data(), const_cast<char*>("session"),
                                        const_cast<char*>("--gui"), gui.data(),
                                        const_cast<char*>("--")};
            for (int i = 1; i < argc; ++i)
                args.push_back(argv[i]);
            args.push_back(nullptr);
            execv(binary.constData(), args.data());
            std::perror("gorganizerctl session");
            return 1;
        }
    }
    app.setApplicationName("gorganizer");
    app.setOrganizationName("gorganizer");
    app.setApplicationVersion(GORGANIZER_VERSION);

    if (QStyle* fusion = makeFusionStyle())
        app.setStyle(fusion);
    else
        qWarning("gorganizer: Fusion style unavailable — Qt6 base install may be incomplete");

    gorganizer::AppConfig config;
    gorganizer::ThemeManager::applyMode(config.appearanceMode(), config.preferredStyle());

#if QT_VERSION >= QT_VERSION_CHECK(6, 5, 0)
    QObject::connect(QGuiApplication::styleHints(),
                     &QStyleHints::colorSchemeChanged, &app,
                     [&config](Qt::ColorScheme) {
                         if (config.appearanceMode() == "system")
                             gorganizer::ThemeManager::applyMode(
                                 "system", config.preferredStyle());
                     });
#endif

    qint64 daemonPid = 0;
    bool daemonOwned = false;

    QString sock = socketPath();

    bool alreadyRunning = QFileInfo::exists(sock);

    if (!supervised && !alreadyRunning) {
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
                        "  $XDG_STATE_HOME/gorganizer/daemon.log\n"
                        "  (or ~/.local/state/gorganizer/daemon.log)").arg(lastStepSeen));
            if (daemonOwned) {
                gorganizer::GrpcError shutdownErr;
                grpcClient.shutdownDaemonSync(3000, 10000, shutdownErr);
            }
            return 1;
        }
    }

    if (config.isFirstBoot()) {
        gorganizer::SetupWizard wizard(config, &grpcClient);
        if (wizard.exec() == QDialog::Rejected) {
            if (daemonOwned) {
                gorganizer::GrpcError shutdownErr;
                grpcClient.shutdownDaemonSync(3000, 10000, shutdownErr);
            }
            return 0;
        }
    }

    gorganizer::MainWindow mainWindow(config, &grpcClient);
    mainWindow.setDaemonOwned(daemonOwned);
    mainWindow.show();

    int exitCode = app.exec();

    if (daemonOwned) {
        gorganizer::GrpcError shutdownErr;
        if (!grpcClient.shutdownDaemonSync(3000, 10000, shutdownErr)) {
            qWarning("daemon shutdown not confirmed: %s — relying on shell wrapper to reap it",
                     qUtf8Printable(shutdownErr.message));
        }
    }

    return exitCode;
}
