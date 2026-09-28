#include "SetupWizard.h"
#include "GameDetector.h"
#include "DirectoryManager.h"
#include "Dialogs.h"
#include "GrpcClient.h"
#include "ErrorPresenter.h"

#include <QLabel>
#include <QListWidget>
#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QFileDialog>
#include <QPushButton>
#include <QLineEdit>
#include <QFormLayout>
#include <QMenu>
#include <QDir>
#include <algorithm>

namespace gorganizer {

SetupWizard::SetupWizard(AppConfig& config, GrpcClient* grpc, QWidget* parent)
    : QWizard(parent)
    , m_config(config)
    , m_grpc(grpc)
{
    setWindowTitle("Gorganizer Setup");
    setMinimumSize(640, 480);

    addPage(createWelcomePage());
    addPage(createSteamDetectionPage());
    addPage(createGameSelectionPage());
    addPage(createApiKeyPage());
    addPage(createDirectorySetupPage());
    addPage(createFinishPage());

    connect(m_grpc, &GrpcClient::nexusKeySaveFinished, this,
        [this](quint64 requestId, bool saved, const QString& error) {
            if (requestId != m_keyRequestId || currentId() != 3)
                return;
            m_keyRequestId = 0;
            m_apiKeySaveBtn->setEnabled(true);
            m_apiKeySaveBtn->setText("Save Nexus key");
            if (saved) {
                m_apiKeySaved = true;
                m_apiKeyStatus->setText("Nexus key saved.");
            } else {
                if (error == QLatin1String("invalid API key"))
                    m_apiKeyStatus->setText("This key was not accepted by Nexus Mods. Check it and try again.");
                else if (error.startsWith(QLatin1String("saving config:")))
                    m_apiKeyStatus->setText("Couldn't save the key to Gorganizer's settings. Try again later.");
                else
                    m_apiKeyStatus->setText("Couldn't reach Nexus Mods. You can add the key later in Tools → Settings.");
            }
        });
    connect(m_grpc, &GrpcClient::gameConfigurationFinished, this,
        [this](quint64 requestId, const QString&, bool ok, const QString& error) {
            if (requestId == m_manualRequestId) {
                m_manualRequestId = 0;
                m_manualLocateBtn->setEnabled(true);
                if (currentId() == 1)
                    button(QWizard::NextButton)->setEnabled(true);
                if (!ok) {
                    if (currentId() == 1)
                        presentError(this, "Couldn't add game", "add this game", error, true);
                    return;
                }
                auto it = std::find_if(m_manualGames.begin(), m_manualGames.end(),
                    [this](const GameInfo& game) { return game.shortName == m_pendingManualGame.shortName; });
                if (it == m_manualGames.end())
                    m_manualGames.push_back(m_pendingManualGame);
                else
                    *it = m_pendingManualGame;
                if (currentId() == 1) {
                    refreshDetectedGames();
                    m_steamPathLabel->setText(QString("%1 added.").arg(m_pendingManualGame.name));
                }
            } else if (requestId == m_finishRequestId && m_finishing && currentId() == 5) {
                m_finishRequestId = 0;
                if (!ok)
                    m_finishErrors.append(m_selectedGames[m_finishIndex].name + ": "
                        + errorSummary("save this game", error, true) + "\nDetails: " + error);
                ++m_finishIndex;
                configureNextGame();
            }
        });
    connect(m_grpc, &GrpcClient::disconnected, this, [this] {
        if (m_keyRequestId && currentId() == 3) {
            m_keyRequestId = 0;
            m_apiKeySaveBtn->setText("Save Nexus key");
            m_apiKeySaveBtn->setEnabled(true);
            m_apiKeyStatus->setText("Couldn't reach Nexus Mods. You can add the key later in Tools → Settings.");
        }
        if (m_manualRequestId && currentId() == 1) {
            m_manualRequestId = 0;
            m_manualLocateBtn->setEnabled(true);
            button(QWizard::NextButton)->setEnabled(true);
            dialogs::warn(this, "Couldn't add game", "The background service disconnected. Try again.");
        }
        if (m_finishing && currentId() == 5) {
            m_finishing = false;
            m_finishRequestId = 0;
            button(QWizard::FinishButton)->setEnabled(true);
            button(QWizard::BackButton)->setEnabled(true);
            m_finishStatus->setText("Couldn't finish setup. Your selections are still available.\nThe background service disconnected. Try again.");
        }
    });
}

void SetupWizard::accept()
{
    if (m_finishing || currentId() != 5)
        return;
    m_finishing = true;
    m_finishIndex = 0;
    m_finishErrors.clear();
    m_finishStatus->setText("Saving your games…");
    button(QWizard::FinishButton)->setEnabled(false);
    button(QWizard::BackButton)->setEnabled(false);
    configureNextGame();
}

void SetupWizard::reject()
{
    m_keyRequestId = 0;
    m_manualRequestId = 0;
    m_finishRequestId = 0;
    m_finishing = false;
    QWizard::reject();
}

void SetupWizard::configureNextGame()
{
    if (m_finishIndex < m_selectedGames.size()) {
        const auto& game = m_selectedGames[m_finishIndex];
        m_finishRequestId = m_grpc->configureGameTracked(game.shortName, game.name, game.appId,
            QString::fromStdString(game.installDir.string()), game.dataSubpath);
        return;
    }
    if (!m_finishErrors.isEmpty()) {
        m_finishing = false;
        button(QWizard::FinishButton)->setEnabled(true);
        button(QWizard::BackButton)->setEnabled(true);
        m_finishStatus->setText("Couldn't finish setup. Your selections are still available.\n"
            + m_finishErrors.join("\n"));
        return;
    }
    std::vector<QString> shortNames;
    for (const auto& game : m_selectedGames)
        shortNames.push_back(game.shortName);
    m_config.setManagedGames(shortNames);
    if (!m_selectedGames.empty())
        m_config.setActiveGameShortName(m_selectedGames.front().shortName);
    m_config.markSetupComplete();
    QWizard::accept();
}

QWizardPage* SetupWizard::createWelcomePage()
{
    auto* page = new QWizardPage;
    page->setTitle("Welcome to Gorganizer");
    page->setSubTitle("A native Linux mod organizer for Bethesda games");

    auto* layout = new QVBoxLayout(page);
    auto* label = new QLabel(
        "Gorganizer manages mods for Bethesda games running through Steam and Proton.\n\n"
        "It creates a virtual file system overlay so you can enable, disable, and "
        "reorder mods without modifying the original game files.\n\n"
        "This wizard will scan your system for installed games, let you paste a "
        "Nexus Mods API key, and set up the required directories.\n\n"
        "Script extenders (xNVSE, SKSE64, FOSE, F4SE) are installed on-demand "
        "from the main window's Run dropdown once setup is complete.");
    label->setWordWrap(true);
    layout->addWidget(label);
    layout->addStretch();
    return page;
}

QWizardPage* SetupWizard::createSteamDetectionPage()
{
    auto* page = new QWizardPage;
    page->setTitle("Steam Detection");
    page->setSubTitle("Locating your Steam installation and installed games");

    auto* layout = new QVBoxLayout(page);

    m_steamPathLabel = new QLabel("Searching...");
    layout->addWidget(m_steamPathLabel);

    m_detectedList = new QListWidget;
    m_detectedList->setSelectionMode(QAbstractItemView::NoSelection);
    layout->addWidget(m_detectedList);

    auto* btnRow = new QHBoxLayout;
    m_manualLocateBtn = new QPushButton("Locate game…");
    m_manualLocateBtn->setToolTip("Choose a supported game's program file.");
    btnRow->addWidget(m_manualLocateBtn);
    btnRow->addStretch();
    layout->addLayout(btnRow);

    connect(m_manualLocateBtn, &QPushButton::clicked, this, [this]() {
        QString start = QDir::homePath();
        QString path = QFileDialog::getOpenFileName(
            this, "Select a game executable", start,
            "All files (*);;Windows executables (*.exe)");
        if (path.isEmpty())
            return;

        auto detected = GameDetector::fromExecutable(std::filesystem::path(path.toStdString()));
        if (!detected) {
            dialogs::warn(this, "Unsupported game", "This file is not a supported game executable.");
            return;
        }
        m_pendingManualGame = *detected;
        m_manualLocateBtn->setEnabled(false);
        button(QWizard::NextButton)->setEnabled(false);
        m_steamPathLabel->setText("Adding game…");
        m_manualRequestId = m_grpc->configureGameTracked(detected->shortName, detected->name,
            detected->appId, QString::fromStdString(detected->installDir.string()),
            detected->dataSubpath);
    });

    connect(this, &QWizard::currentIdChanged, this, [this](int id) {
        if (id != 1) return;

        auto root = GameDetector::findSteamRoot();
        if (!root) {
            m_steamPathLabel->setText("Steam not found. You can locate a game below.");
            m_detectedGames.clear();
        } else {
            m_steamPathLabel->setText("Steam installations found. Searching all game libraries.");
            m_detectedGames = GameDetector::detectAll();
        }
        refreshDetectedGames();
    });

    return page;
}

void SetupWizard::refreshDetectedGames()
{
    for (const auto& manual : m_manualGames) {
        auto it = std::find_if(m_detectedGames.begin(), m_detectedGames.end(),
            [&manual](const GameInfo& game) { return game.shortName == manual.shortName; });
        if (it == m_detectedGames.end())
            m_detectedGames.push_back(manual);
        else
            *it = manual;
    }
    m_detectedList->clear();
    if (m_detectedGames.empty()) {
        m_detectedList->addItem("No supported games found in Steam. Use Locate game… to add one.");
        return;
    }
    for (const auto& game : m_detectedGames)
        m_detectedList->addItem(QString("%1 (%2)").arg(game.name,
            QString::fromStdString(game.installDir.string())));
}

class GameSelectionPage : public QWizardPage {
public:
    GameSelectionPage(QListWidget*& listRef) : m_listRef(listRef) {}
    bool isComplete() const override
    {
        if (!m_listRef) return false;
        for (int i = 0; i < m_listRef->count(); ++i) {
            if (m_listRef->item(i)->checkState() == Qt::Checked)
                return true;
        }
        return false;
    }

private:
    QListWidget*& m_listRef;
};

QWizardPage* SetupWizard::createGameSelectionPage()
{
    auto* page = new GameSelectionPage(m_selectionList);
    page->setTitle("Game Selection");
    page->setSubTitle("Select which games you want to manage. Right-click for quick actions.");

    auto* layout = new QVBoxLayout(page);

    m_selectionList = new QListWidget;
    m_selectionList->setContextMenuPolicy(Qt::CustomContextMenu);
    layout->addWidget(m_selectionList);

    connect(m_selectionList, &QListWidget::customContextMenuRequested, this,
        [this, page](const QPoint& pos) {
            QMenu menu;
            auto* selAll = menu.addAction("Select All");
            auto* selNone = menu.addAction("Select None");
            QAction* chosen = menu.exec(m_selectionList->viewport()->mapToGlobal(pos));
            if (!chosen) return;
            Qt::CheckState state = (chosen == selAll) ? Qt::Checked : Qt::Unchecked;
            for (int i = 0; i < m_selectionList->count(); ++i)
                m_selectionList->item(i)->setCheckState(state);
            emit page->completeChanged();
        });

    connect(this, &QWizard::currentIdChanged, this, [this, page](int id) {
        if (id != 2) return;

        m_selectionList->clear();
        bool hasFO3 = false, hasFNV = false;
        bool sawTTW = false;
        for (const auto& game : m_detectedGames) {
            QString label = game.name;
            if (game.shortName == "ttw") {
                sawTTW = true;
                label += " (install required after setup)";
            }
            auto* item = new QListWidgetItem(label, m_selectionList);
            item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
            item->setCheckState(Qt::Checked);
            item->setData(Qt::UserRole, game.shortName);
            if (game.shortName == "fallout3") hasFO3 = true;
            if (game.shortName == "falloutnv") hasFNV = true;
        }
        if (hasFO3 && hasFNV && !sawTTW) {
            auto* item = new QListWidgetItem(
                "Tale of Two Wastelands (install required after setup)",
                m_selectionList);
            item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
            item->setCheckState(Qt::Unchecked);
            item->setData(Qt::UserRole, QString("ttw"));
        } else if (!sawTTW) {
            auto* item = new QListWidgetItem(
                "Tale of Two Wastelands — requires Fallout 3 and Fallout: New Vegas",
                m_selectionList);
            item->setFlags(item->flags() & ~Qt::ItemIsEnabled);
            item->setData(Qt::UserRole, QString("ttw-disabled"));
            item->setToolTip("Install both Fallout 3 and Fallout: New Vegas via Steam to enable.");
        }
        emit page->completeChanged();
    });

    connect(m_selectionList, &QListWidget::itemChanged, page, [page]() {
        emit page->completeChanged();
    });

    return page;
}

QWizardPage* SetupWizard::createApiKeyPage()
{
    auto* page = new QWizardPage;
    page->setTitle("Nexus Mods API Key");
    page->setSubTitle("Optional — required for downloads and script extender install");

    auto* layout = new QVBoxLayout(page);

    auto* help = new QLabel(
        "Paste your Nexus Mods personal API key below. Save it to use Nexus Mods downloads.\n\n"
        "You can skip this step and paste the key later in Tools → Settings.");
    help->setWordWrap(true);
    layout->addWidget(help);

    auto* linkLabel = new QLabel(
        "<a href=\"https://www.nexusmods.com/users/myaccount?tab=api+access\">"
        "Get your API key from Nexus Mods</a>");
    linkLabel->setOpenExternalLinks(true);
    layout->addWidget(linkLabel);

    auto* form = new QFormLayout;
    m_apiKeyEdit = new QLineEdit;
    m_apiKeyEdit->setPlaceholderText("Paste your Nexus Mods API key");
    m_apiKeyEdit->setEchoMode(QLineEdit::Password);
    form->addRow("API Key:", m_apiKeyEdit);
    layout->addLayout(form);

    auto* btnRow = new QHBoxLayout;
    m_apiKeySaveBtn = new QPushButton("Save Nexus key");
    btnRow->addWidget(m_apiKeySaveBtn);
    btnRow->addStretch();
    layout->addLayout(btnRow);

    m_apiKeyStatus = new QLabel;
    m_apiKeyStatus->setTextFormat(Qt::PlainText);
    m_apiKeyStatus->setWordWrap(true);
    layout->addWidget(m_apiKeyStatus);
    layout->addStretch();

    connect(m_apiKeyEdit, &QLineEdit::textChanged, this, [this] {
        m_apiKeySaved = false;
        m_keyRequestId = 0;
        m_apiKeyStatus->clear();
        m_apiKeySaveBtn->setText("Save Nexus key");
        m_apiKeySaveBtn->setEnabled(true);
    });
    connect(m_apiKeySaveBtn, &QPushButton::clicked, this, [this] {
        QString key = m_apiKeyEdit->text().trimmed();
        if (key.isEmpty()) {
            m_apiKeyStatus->setText("Please enter a key.");
            return;
        }
        m_apiKeySaveBtn->setEnabled(false);
        m_apiKeySaveBtn->setText("Saving…");
        m_apiKeyStatus->clear();
        m_keyRequestId = m_grpc->saveNexusAPIKey(key);
    });
    connect(this, &QWizard::currentIdChanged, this, [this](int id) {
        if (id == 3 || !m_keyRequestId)
            return;
        m_keyRequestId = 0;
        m_apiKeySaveBtn->setEnabled(true);
        m_apiKeySaveBtn->setText("Save Nexus key");
    });

    return page;
}

QWizardPage* SetupWizard::createDirectorySetupPage()
{
    auto* page = new QWizardPage;
    page->setTitle("Directory Setup");
    page->setSubTitle("Creating mod management directories");

    auto* layout = new QVBoxLayout(page);
    m_dirStatusLabel = new QLabel;
    m_dirStatusLabel->setWordWrap(true);
    layout->addWidget(m_dirStatusLabel);
    layout->addStretch();

    connect(this, &QWizard::currentIdChanged, this, [this](int id) {
        if (id != 4) return;

        m_selectedGames.clear();
        for (int i = 0; i < m_selectionList->count(); ++i) {
            auto* item = m_selectionList->item(i);
            if (item->checkState() != Qt::Checked)
                continue;
            QString shortName = item->data(Qt::UserRole).toString();
            if (shortName == "ttw-disabled")
                continue;
            auto it = std::find_if(m_detectedGames.begin(), m_detectedGames.end(),
                [&shortName](const GameInfo& g) { return g.shortName == shortName; });
            if (it != m_detectedGames.end()) {
                m_selectedGames.push_back(*it);
                continue;
            }
            if (auto known = GameInfo::findByShortName(shortName)) {
                if (known->synthetic) {
                    auto parent = std::find_if(m_detectedGames.begin(), m_detectedGames.end(),
                        [&known](const GameInfo& game) { return game.shortName == known->linkedFromShortName; });
                    if (parent != m_detectedGames.end()) {
                        known->installDir = parent->installDir;
                        known->dataDir = parent->dataDir;
                    }
                }
                m_selectedGames.push_back(*known);
            }
        }

        auto configDir = m_config.configDir();
        auto dataDir = m_config.dataDir();

        QString status;
        bool ok = DirectoryManager::createBaseDirectories(configDir, dataDir);
        if (!ok) {
            status = "Failed to create base directories.\n";
        } else {
            status = "Created base directories:\n"
                     "  " + QString::fromStdString(configDir.string()) + "\n"
                     "  " + QString::fromStdString(dataDir.string()) + "\n\n";
        }

        for (const auto& game : m_selectedGames) {
            bool gameOk = DirectoryManager::createGameDirectories(game, dataDir);
            auto gameDir = dataDir / game.shortName.toStdString();
            if (gameOk) {
                status += "Created directories for " + game.name + ":\n"
                          "  " + QString::fromStdString(gameDir.string()) + "/mods/\n"
                          "  " + QString::fromStdString(gameDir.string()) + "/profiles/Default/\n"
                          "  " + QString::fromStdString(gameDir.string()) + "/overwrite/\n\n";
            } else {
                status += "Failed to create directories for " + game.name + "\n\n";
            }
        }
        m_dirStatusLabel->setText(status);
    });

    return page;
}

QWizardPage* SetupWizard::createFinishPage()
{
    auto* page = new QWizardPage;
    page->setTitle("Finish setup");
    page->setSubTitle("Save your choices to the background service");

    auto* layout = new QVBoxLayout(page);
    m_summaryLabel = new QLabel;
    m_summaryLabel->setWordWrap(true);
    layout->addWidget(m_summaryLabel);
    m_finishStatus = new QLabel;
    m_finishStatus->setTextFormat(Qt::PlainText);
    m_finishStatus->setWordWrap(true);
    layout->addWidget(m_finishStatus);
    layout->addStretch();

    connect(this, &QWizard::currentIdChanged, this, [this](int id) {
        if (id != 5) return;
        m_finishStatus->clear();
        QString apiKeyMsg = m_apiKeySaved
            ? "Your Nexus key has been saved."
            : "No Nexus key saved — you can add one in Tools → Settings later.";
        m_summaryLabel->setText(
            QString("Ready to manage %1 game(s). Click Finish to save your games.\n\n%2\n\n"
                    "Script extenders (xNVSE, SKSE64, F4SE, FOSE) can be installed "
                    "directly from the main window: pick the extender in the Run "
                    "dropdown and the first click downloads + installs it. Next "
                    "click runs the game through it.\n\n"
                    "Use Install Mod or drop an archive onto the window to install it. "
                    "Then tick the mod to enable it.")
                .arg(m_selectedGames.size())
                .arg(apiKeyMsg));
    });

    return page;
}

}
