#pragma once

#include <QDialog>
#include <QString>
#include "GrpcClient.h"

class QStackedWidget;
class QPushButton;
class QLabel;
class QPlainTextEdit;
class QProgressBar;
class QRadioButton;
class QListWidget;
class QLineEdit;
class QFormLayout;

namespace gorganizer {

class GrpcClient;
class GameInfo;

// Modal six-page wizard for installing Tale of Two Wastelands; drives daemon-side TTW RPC flow.
class TTWInstallDialog : public QDialog {
    Q_OBJECT
public:
    TTWInstallDialog(GrpcClient* grpc, QString fnvShortName,
                     QString currentProfile, QWidget* parent = nullptr);

    enum Outcome { Rejected = 0, InstalledOnly = 1, Accepted = 2 };
    Outcome outcome() const { return m_outcome; }

    QString chosenModName() const { return m_modName; }

protected:
    void closeEvent(QCloseEvent* ev) override;
    void reject() override;

private slots:
    void onBackendChanged();
    void onRefreshPrereqs();
    void onInstallMissing();
    void onBootstrapPrefix();
    void onUninstallMono();
    void onPickMpi();
    void onConfigure();
    void onRunInstaller();
    void onCancelInstaller();
    void onPickLauncher();
    void onActivate();
    void onDaemonInfo(const QString& info);

private:
    QWidget* buildBackendPage();
    QWidget* buildPrereqsPage();
    QWidget* buildMpiPage();
    QWidget* buildConfigurePage();
    QWidget* buildRunPage();
    QWidget* buildLauncherPage();

    void renderPrereqs(const GrpcTTWPrereqStatus& st);
    void appendLog(const QString& line);
    void setNavButtons(bool backEnabled, bool nextEnabled, const QString& nextText = "Next");
    int currentBackend() const;
    QString humanBytes(int64_t b) const;
    void populateLauncherCandidates();

    GrpcClient* m_grpc = nullptr;
    QString m_fnvShortName;
    QString m_currentProfile;
    Outcome m_outcome = Rejected;
    QString m_modName = "Tale of Two Wastelands";
    QString m_mpiPath;
    QString m_installerExe;
    QString m_installVersion;
    QStringList m_alternateMpis;
    QString m_inFlightInstallId;
    QString m_lastFinishedInstallId;
    QString m_chosenLauncherRel = "nvse_loader.exe";
    bool m_installRunning = false;

    QStackedWidget* m_stack = nullptr;
    QRadioButton* m_radioNative = nullptr;
    QRadioButton* m_radioWine = nullptr;

    QListWidget* m_prereqList = nullptr;
    QPushButton* m_installMissingBtn = nullptr;
    QPushButton* m_bootstrapBtn = nullptr;
    QPushButton* m_uninstallMonoBtn = nullptr;
    QPushButton* m_refreshPrereqsBtn = nullptr;
    bool m_lastPrereqsAllGreen = false;

    QLineEdit* m_mpiLineEdit = nullptr;
    QListWidget* m_alternateMpiList = nullptr;

    QLineEdit* m_modNameEdit = nullptr;
    QFormLayout* m_pathsForm = nullptr;
    QLabel* m_summaryLabel = nullptr;

    QProgressBar* m_runProgress = nullptr;
    QPlainTextEdit* m_runLog = nullptr;
    QPushButton* m_runStartBtn = nullptr;
    QPushButton* m_runCancelBtn = nullptr;
    QLabel* m_runStatusLine = nullptr;
    QLabel* m_runElapsedLabel = nullptr;
    class QTimer* m_runTicker = nullptr;
    qint64 m_runStartedMsecs = 0;

    QListWidget* m_launcherList = nullptr;

    QPushButton* m_backBtn = nullptr;
    QPushButton* m_nextBtn = nullptr;
    QPushButton* m_cancelBtn = nullptr;
};

} // namespace gorganizer
