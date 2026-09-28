#pragma once

#include <QWizard>
#include <QStringList>
#include "AppConfig.h"
#include "GameInfo.h"
#include <vector>

class QLabel;
class QListWidget;
class QLineEdit;
class QPushButton;

namespace gorganizer {

class GrpcClient;

class SetupWizard : public QWizard {
    Q_OBJECT
public:
    explicit SetupWizard(AppConfig& config, GrpcClient* grpc, QWidget* parent = nullptr);

private:
    void accept() override;
    void reject() override;

    QWizardPage* createWelcomePage();
    QWizardPage* createSteamDetectionPage();
    QWizardPage* createGameSelectionPage();
    QWizardPage* createApiKeyPage();
    QWizardPage* createDirectorySetupPage();
    QWizardPage* createFinishPage();

    void refreshDetectedGames();
    void configureNextGame();

    AppConfig& m_config;
    GrpcClient* m_grpc;
    std::vector<GameInfo> m_detectedGames;
    std::vector<GameInfo> m_manualGames;
    std::vector<GameInfo> m_selectedGames;
    GameInfo m_pendingManualGame;
    QLabel* m_steamPathLabel = nullptr;
    QListWidget* m_detectedList = nullptr;
    QPushButton* m_manualLocateBtn = nullptr;
    QListWidget* m_selectionList = nullptr;
    QLineEdit* m_apiKeyEdit = nullptr;
    QLabel* m_apiKeyStatus = nullptr;
    QPushButton* m_apiKeySaveBtn = nullptr;
    quint64 m_keyRequestId = 0;
    bool m_apiKeySaved = false;
    QLabel* m_dirStatusLabel = nullptr;
    QLabel* m_summaryLabel = nullptr;
    QLabel* m_finishStatus = nullptr;
    quint64 m_manualRequestId = 0;
    quint64 m_finishRequestId = 0;
    size_t m_finishIndex = 0;
    QStringList m_finishErrors;
    bool m_finishing = false;
};

}
