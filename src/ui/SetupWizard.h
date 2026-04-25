#pragma once

#include <QWizard>
#include "AppConfig.h"
#include "GameInfo.h"
#include <vector>

class QLabel;
class QListWidget;
class QLineEdit;
class QPushButton;

namespace gorganizer {

class SetupWizard : public QWizard {
    Q_OBJECT
public:
    explicit SetupWizard(AppConfig& config, QWidget* parent = nullptr);

    std::vector<GameInfo> selectedGames() const { return m_selectedGames; }
    // Returns the validated API key the user pasted in the wizard, or an
    // empty string if they skipped that page. Caller should hand this to
    // GrpcClient::setNexusAPIKey() once the daemon is reachable so the
    // running daemon picks up the key without a restart — saving to disk
    // alone leaves the daemon's in-memory copy stale (and downloadMgr nil).
    QString validatedApiKey() const { return m_apiKeyValid ? m_validatedApiKey : QString(); }

private:
    void accept() override;

    QWizardPage* createWelcomePage();
    QWizardPage* createSteamDetectionPage();
    QWizardPage* createGameSelectionPage();
    QWizardPage* createApiKeyPage();
    QWizardPage* createDirectorySetupPage();
    QWizardPage* createFinishPage();

    void validateApiKey(const QString& key);

    AppConfig& m_config;
    std::vector<GameInfo> m_detectedGames;
    std::vector<GameInfo> m_selectedGames;
    QLabel* m_steamPathLabel = nullptr;
    QListWidget* m_detectedList = nullptr;
    QPushButton* m_manualLocateBtn = nullptr;
    QListWidget* m_selectionList = nullptr;
    QLineEdit* m_apiKeyEdit = nullptr;
    QLabel* m_apiKeyStatus = nullptr;
    QPushButton* m_apiKeyValidateBtn = nullptr;
    bool m_apiKeyValid = false;
    QString m_validatedApiKey;
    QLabel* m_dirStatusLabel = nullptr;
    QLabel* m_summaryLabel = nullptr;
};

} // namespace gorganizer
