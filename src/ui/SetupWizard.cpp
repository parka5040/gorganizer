#include "SetupWizard.h"
#include "GameDetector.h"
#include "DirectoryManager.h"
#include "ThemeManager.h"
#include "Dialogs.h"

#include <QLabel>
#include <QListWidget>
#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QFileDialog>
#include <QPushButton>
#include <QLineEdit>
#include <QFormLayout>
#include <QMenu>
#include <QDesktopServices>
#include <QUrl>
#include <QNetworkAccessManager>
#include <QNetworkRequest>
#include <QNetworkReply>
#include <QEventLoop>
#include <QJsonDocument>
#include <QJsonObject>
#include <QFile>
#include <QDir>
#include <QSysInfo>

namespace {

// Persists the API key to the daemon's config.json directly (wizard runs before daemon spawn).
bool saveNexusApiKeyToConfig(const QString& apiKey)
{
    QString configDir = QString::fromUtf8(qgetenv("XDG_CONFIG_HOME"));
    if (configDir.isEmpty())
        configDir = QDir::homePath() + "/.config";
    configDir += "/gorganizer";
    QDir().mkpath(configDir);

    QString path = configDir + "/config.json";
    QJsonObject root;
    QFile f(path);
    if (f.exists() && f.open(QIODevice::ReadOnly)) {
        auto doc = QJsonDocument::fromJson(f.readAll());
        if (doc.isObject())
            root = doc.object();
        f.close();
    }
    root.insert("nexus_api_key", apiKey);
    if (!root.contains("games"))
        root.insert("games", QJsonObject{});
    if (!root.contains("log_level"))
        root.insert("log_level", "info");

    if (!f.open(QIODevice::WriteOnly | QIODevice::Truncate))
        return false;
    f.write(QJsonDocument(root).toJson(QJsonDocument::Indented));
    f.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner);
    f.close();
    return true;
}

}

namespace gorganizer {

namespace {
// Status-text hues from the active theme so they read in both light and dark.
QString okHex() { return ThemeManager::currentPalette().successFg.name(); }
QString errHex() { return ThemeManager::currentPalette().errorFg.name(); }
QString mutedHex() { return ThemeManager::currentPalette().textMuted.name(); }
}

SetupWizard::SetupWizard(AppConfig& config, QWidget* parent)
    : QWizard(parent)
    , m_config(config)
{
    setWindowTitle("Gorganizer Setup");
    setMinimumSize(640, 480);

    addPage(createWelcomePage());
    addPage(createSteamDetectionPage());
    addPage(createGameSelectionPage());
    addPage(createApiKeyPage());
    addPage(createDirectorySetupPage());
    addPage(createFinishPage());
}

void SetupWizard::accept()
{
    m_config.markSetupComplete();

    std::vector<QString> shortNames;
    for (const auto& g : m_selectedGames)
        shortNames.push_back(g.shortName);
    m_config.setManagedGames(shortNames);

    if (!m_selectedGames.empty())
        m_config.setActiveGameShortName(m_selectedGames.front().shortName);

    if (m_apiKeyValid && !m_validatedApiKey.isEmpty())
        saveNexusApiKeyToConfig(m_validatedApiKey);

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
    m_manualLocateBtn = new QPushButton("Manually locate game executable...");
    m_manualLocateBtn->setToolTip(
        "Pick the .exe file for a Bethesda game (e.g. SkyrimSE.exe, Fallout4.exe, OblivionRemastered.exe). "
        "Use this for Lutris, GOG, or any install Steam does not recognize. "
        "Steam is still required for launching.");
    btnRow->addWidget(m_manualLocateBtn);
    btnRow->addStretch();
    layout->addLayout(btnRow);

    connect(m_manualLocateBtn, &QPushButton::clicked, this, [this]() {
        QString start = QDir::homePath();
        QString path = QFileDialog::getOpenFileName(
            this, "Select a game executable", start,
            "Windows executables (*.exe);;All files (*)");
        if (path.isEmpty())
            return;

        auto detected = GameDetector::fromExecutable(std::filesystem::path(path.toStdString()));
        if (!detected) {
            dialogs::warn(this, "Unrecognized Executable",
                "That file doesn't match any known Bethesda game. "
                "Expected one of: Morrowind.exe, Oblivion.exe, TESV.exe, SkyrimSE.exe, "
                "Fallout3.exe, FalloutNV.exe, Fallout4.exe, Starfield.exe, OblivionRemastered.exe, "
                "OblivionRemastered-Win64-Shipping.exe.");
            return;
        }

        bool exists = std::any_of(m_detectedGames.begin(), m_detectedGames.end(),
            [&](const GameInfo& g) { return g.appId == detected->appId; });
        if (exists) {
            dialogs::info(this, "Already detected",
                QString("%1 is already in the list.").arg(detected->name));
            return;
        }

        m_detectedGames.push_back(*detected);
        m_detectedList->addItem(
            QString("%1 (manually located: %2)")
                .arg(detected->name)
                .arg(QString::fromStdString(detected->installDir.string())));
    });

    connect(this, &QWizard::currentIdChanged, this, [this](int id) {
        if (id != 1) return;

        auto root = GameDetector::findSteamRoot();
        if (!root) {
            m_steamPathLabel->setText(
                "Steam installation not found. You can still add games "
                "manually using the button below.");
        } else {
            m_steamPathLabel->setText("Steam found at: " + QString::fromStdString(root->string()));
            auto folders = GameDetector::findLibraryFolders(*root);
            m_detectedGames = GameDetector::detectGames(folders);
        }

        m_detectedList->clear();
        if (m_detectedGames.empty()) {
            m_detectedList->addItem(
                "No supported Bethesda games found in Steam. "
                "Use 'Manually locate' to add one.");
        } else {
            for (const auto& game : m_detectedGames) {
                m_detectedList->addItem(
                    QString("%1 (App ID: %2)").arg(game.name).arg(game.appId));
            }
        }
    });

    return page;
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
        "Paste your Nexus Mods personal API key below. The key is saved to the "
        "daemon's config and used to authenticate NXM downloads.\n\n"
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
    m_apiKeyValidateBtn = new QPushButton("Validate && Save");
    btnRow->addWidget(m_apiKeyValidateBtn);
    btnRow->addStretch();
    layout->addLayout(btnRow);

    m_apiKeyStatus = new QLabel;
    m_apiKeyStatus->setWordWrap(true);
    layout->addWidget(m_apiKeyStatus);
    layout->addStretch();

    connect(m_apiKeyValidateBtn, &QPushButton::clicked, this, [this]() {
        QString key = m_apiKeyEdit->text().trimmed();
        if (key.isEmpty()) {
            m_apiKeyStatus->setText(
                QString("<span style='color:%1;'>Please enter a key.</span>").arg(errHex()));
            return;
        }
        validateApiKey(key);
    });

    return page;
}

void SetupWizard::validateApiKey(const QString& key)
{
    m_apiKeyStatus->setText(QString("<span style='color:%1;'>Validating...</span>").arg(mutedHex()));
    m_apiKeyValidateBtn->setEnabled(false);

    QNetworkAccessManager manager;
    QNetworkRequest req(QUrl("https://api.nexusmods.com/v3/games/skyrimspecialedition/mods/12604"));
    req.setRawHeader("apikey", key.toUtf8());
    req.setRawHeader("User-Agent",
        QString("Gorganizer/0.1.0 (%1) Qt").arg(QSysInfo::productType()).toUtf8());
    req.setRawHeader("Application-Name", "Gorganizer");
    req.setRawHeader("Application-Version", "0.1.0");
    req.setRawHeader("Protocol-Version", "1.0.0");
    req.setRawHeader("Content-Type", "application/json");

    QEventLoop loop;
    QNetworkReply* reply = manager.get(req);
    QObject::connect(reply, &QNetworkReply::finished, &loop, &QEventLoop::quit);
    loop.exec();

    int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
    QByteArray body = reply->readAll();
    reply->deleteLater();
    m_apiKeyValidateBtn->setEnabled(true);

    if (status == 200) {
        m_apiKeyValid = true;
        m_validatedApiKey = key;
        m_apiKeyStatus->setText(
            QString("<b style='color:%1;'>Validated — the key will be saved on finish.</b>").arg(okHex()));
    } else if (status == 401 || status == 403) {
        m_apiKeyValid = false;
        m_validatedApiKey.clear();
        m_apiKeyStatus->setText(
            QString("<b style='color:%1;'>Key rejected. Check that you copied it correctly.</b>").arg(errHex()));
    } else {
        m_apiKeyValid = false;
        m_validatedApiKey.clear();
        m_apiKeyStatus->setText(
            QString("<b style='color:%1;'>Validation failed (HTTP %2). Network issue?</b>")
                .arg(errHex()).arg(status));
    }
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
            if (auto known = GameInfo::findByShortName(shortName))
                m_selectedGames.push_back(*known);
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
    page->setTitle("Setup Complete");
    page->setSubTitle("Gorganizer is ready to use");

    auto* layout = new QVBoxLayout(page);
    m_summaryLabel = new QLabel;
    m_summaryLabel->setWordWrap(true);
    layout->addWidget(m_summaryLabel);
    layout->addStretch();

    connect(this, &QWizard::currentIdChanged, this, [this](int id) {
        if (id != 5) return;
        QString apiKeyMsg = m_apiKeyValid
            ? "Nexus API key saved."
            : "No Nexus API key set — you can paste one in Tools → Settings later.";
        m_summaryLabel->setText(
            QString("Setup complete. Managing %1 game(s).\n\n%2\n\n"
                    "Script extenders (xNVSE, SKSE64, F4SE, FOSE) can be installed "
                    "directly from the main window: pick the extender in the Run "
                    "dropdown and the first click downloads + installs it. Next "
                    "click runs the game through it.\n\n"
                    "Drop archives into the Downloads tab or double-click to install mods.")
                .arg(m_selectedGames.size())
                .arg(apiKeyMsg));
    });

    return page;
}

}
