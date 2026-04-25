#pragma once

#include <QWidget>
#include <QHash>
#include <QTimer>

#include "GrpcClient.h"

class QLabel;
class QProgressBar;
class QPushButton;

namespace gorganizer {

// InstallStatusBanner is a slim persistent strip at the top of the main
// window that surfaces install activity so the user never has to wonder
// "is anything happening?". It consumes GrpcClient::installProgressEvent
// and renders the most recent still-active install; concurrent installs
// are summarized via a "+N more" badge.
//
// States → UI:
//   EXTRACTING / COPYING / FINALIZING → determinate progress bar + label
//   FOMOD_PENDING                     → warning tint + "Bring wizard to front"
//   COMPLETE / FAILED                 → terminal tint, auto-hides after 2s
class InstallStatusBanner : public QWidget {
    Q_OBJECT
public:
    explicit InstallStatusBanner(GrpcClient* grpc, QWidget* parent = nullptr);

    // For flows that don't yet route through the daemon's InstallProgress
    // (e.g. the client-side FOMOD wizard before B3 lands), callers can
    // push a synthetic pending state so the banner stays informative.
    void showFomodPending(const QString& archiveRelPath, const QString& modName);
    void clearUiNotice(const QString& archiveRelPath);

signals:
    // User clicked "Bring wizard to front" while in FOMOD_PENDING.
    void bringFomodToFront();

private slots:
    void onInstallProgress(const GrpcInstallProgress& p);

private:
    struct ActiveInstall {
        QString archiveRelPath;
        QString modName;
        int step = 0;
        int pct = -1;
        qint64 filesDone = 0;
        qint64 filesTotal = 0;
        QString currentFile;
        QString error;
    };

    void redraw();
    void setActive(bool visible);

    GrpcClient* m_grpc = nullptr;
    QHash<QString, ActiveInstall> m_active;   // archiveRelPath → state
    QString m_focusedKey;                     // most recently updated
    QTimer* m_autoHide = nullptr;

    QLabel* m_titleLabel = nullptr;
    QLabel* m_detailLabel = nullptr;
    QProgressBar* m_bar = nullptr;
    QPushButton* m_fomodBtn = nullptr;
    QLabel* m_extraBadge = nullptr;
};

} // namespace gorganizer
