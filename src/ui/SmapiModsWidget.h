#pragma once

#include <QDateTime>
#include <QWidget>
#include "GrpcTypes.h"

class QLabel;
class QModelIndex;
class QPushButton;
class QTreeView;
class QTreeWidget;

namespace gorganizer {

class SmapiComponentModel;

class SmapiModsWidget : public QWidget {
    Q_OBJECT
public:
    explicit SmapiModsWidget(QWidget* parent = nullptr);

    // Shows a dependency report: versions, warnings, the component table, failed downloads and the toolbar counts.
    void setReport(const GrpcModDependencyReport& report);
    // Clears the table and header for a game or profile that has no report yet.
    void clearReport();
    // Shows why the latest report request failed, or hides the message when error is empty.
    void setReportError(const QString& error);
    // Shows when smapi.io was last reached for the game and the last online-check failure, if any.
    void setRemoteState(const QDateTime& lastChecked, const QString& lastError, bool checking);
    // Shows the offline hint while automatic online checks are disabled.
    void setOnlineChecksHint(bool off);
    // Disables the fetch and enable actions while one of them is running.
    void setActionsBusy(bool busy);
    // Lists downloaded dependencies whose automatic enable gave up, one line each, hiding the section when empty.
    void setWaitingEnables(const QStringList& lines);

signals:
    void refreshRequested();
    void checkUpdatesRequested();
    void fetchMissingRequested();
    void enableRequiredRequested();
    // The user asked to enable the downloaded dependencies whose automatic enable gave up.
    void waitingEnablesRequested();
    // The user asked to fetch a dependency again whose download failed or expired.
    void retryFetchRequested(const QString& uniqueId);

private slots:
    // Opens the update page of a clicked Update cell.
    void onItemClicked(const QModelIndex& index);
    // Asks to fetch the selected failed or expired dependency again.
    void onRetryClicked();

private:
    void updateButtons();
    // Shows the report's failed or expired dependency downloads that are still missing.
    void showRecentFailures(const GrpcModDependencyReport& report);

    SmapiComponentModel* m_model = nullptr;
    QTreeView* m_view = nullptr;
    QLabel* m_versionLabel = nullptr;
    QLabel* m_remoteLabel = nullptr;
    QLabel* m_onlineChecksHint = nullptr;
    QLabel* m_rootManifestLabel = nullptr;
    QLabel* m_errorLabel = nullptr;
    QLabel* m_summaryLabel = nullptr;
    QPushButton* m_refreshButton = nullptr;
    QPushButton* m_checkButton = nullptr;
    QPushButton* m_fetchButton = nullptr;
    QPushButton* m_enableButton = nullptr;
    QWidget* m_waitingBox = nullptr;
    QLabel* m_waitingLabel = nullptr;
    QPushButton* m_waitingButton = nullptr;
    QWidget* m_failuresBox = nullptr;
    QTreeWidget* m_failuresTree = nullptr;
    QPushButton* m_retryButton = nullptr;
    int m_fetchableCount = 0;
    int m_enableCount = 0;
    bool m_haveReport = false;
    bool m_checking = false;
    bool m_actionsBusy = false;
};

}
