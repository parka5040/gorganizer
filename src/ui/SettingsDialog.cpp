#include "SettingsDialog.h"
#include "AppConfig.h"
#include "GrpcClient.h"
#include "ThemeManager.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QFormLayout>
#include <QLineEdit>
#include <QDialogButtonBox>
#include <QLabel>
#include <QPushButton>
#include <QComboBox>
#include <QCheckBox>
#include <QProcess>
#include <QFileInfo>
#include <QFile>
#include <QDir>
#include <QStandardPaths>
#include <QCoreApplication>

namespace gorganizer {

SettingsDialog::SettingsDialog(GrpcClient* grpc, AppConfig* config, QWidget* parent)
    : QDialog(parent)
    , m_grpc(grpc)
    , m_config(config)
{
    setWindowTitle("Settings");
    setMinimumWidth(450);

    auto* layout = new QVBoxLayout(this);

    auto* form = new QFormLayout;

    m_themeCombo = new QComboBox;
    populateThemeCombo();
    connect(m_themeCombo, &QComboBox::currentTextChanged, this, &SettingsDialog::onThemeChanged);
    form->addRow("Theme:", m_themeCombo);

    m_collapseViewsCheck = new QCheckBox("Show one ordering for both views");
    m_collapseViewsCheck->setToolTip(
        "When on, the Separator View checkbox in the mod list is forced on and "
        "disabled, and any reorder writes the same index into both visual_index "
        "and true_index. Toggling off later leaves any cross-stamping in place.");
    if (m_config)
        m_collapseViewsCheck->setChecked(m_config->collapsedSeparatorView());
    connect(m_collapseViewsCheck, &QCheckBox::toggled,
            this, &SettingsDialog::onCollapsedSeparatorViewToggled);
    form->addRow("Mod list:", m_collapseViewsCheck);

    m_apiKeyEdit = new QLineEdit;
    m_apiKeyEdit->setPlaceholderText("Paste your Nexus Mods API key here");
    m_apiKeyEdit->setEchoMode(QLineEdit::Password);
    form->addRow("Nexus API Key:", m_apiKeyEdit);

    auto* helpLabel = new QLabel(
        "<a href=\"https://www.nexusmods.com/users/myaccount?tab=api+access\">"
        "Get your API key from Nexus Mods</a>");
    helpLabel->setOpenExternalLinks(true);
    form->addRow("", helpLabel);

    m_statusLabel = new QLabel;
    form->addRow("", m_statusLabel);

    m_protonCombo = new QComboBox;
    m_protonCombo->setMinimumWidth(240);
    auto* protonRow = new QHBoxLayout;
    protonRow->addWidget(m_protonCombo);
    auto* protonSaveBtn = new QPushButton("Save");
    protonRow->addWidget(protonSaveBtn);
    connect(protonSaveBtn, &QPushButton::clicked, this, &SettingsDialog::onSaveProton);
    form->addRow("Default Proton:", protonRow);

    m_protonStatus = new QLabel;
    form->addRow("", m_protonStatus);

    auto* socketLabel = new QLabel;
    const char* xdg = std::getenv("XDG_RUNTIME_DIR");
    QString socketPath = xdg ? QString("%1/gorganizer/gorganizer.sock").arg(xdg)
                             : QString("/tmp/gorganizer/gorganizer.sock");
    socketLabel->setText(socketPath);
    socketLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
    form->addRow("Daemon Socket:", socketLabel);

    auto* nxmRow = new QHBoxLayout;
    auto* testNxmBtn = new QPushButton("Test NXM Handler");
    auto* reregNxmBtn = new QPushButton("Re-register");
    nxmRow->addWidget(testNxmBtn);
    nxmRow->addWidget(reregNxmBtn);
    nxmRow->addStretch();
    connect(testNxmBtn, &QPushButton::clicked, this, &SettingsDialog::onTestNxm);
    connect(reregNxmBtn, &QPushButton::clicked, this, &SettingsDialog::onReregisterNxm);
    form->addRow("Nexus NXM Handler:", nxmRow);

    m_nxmStatus = new QLabel;
    m_nxmStatus->setTextFormat(Qt::RichText);
    m_nxmStatus->setWordWrap(true);
    form->addRow("", m_nxmStatus);

    layout->addLayout(form);

    populateProtonCombo();

    auto* buttons = new QDialogButtonBox;
    m_saveBtn = static_cast<QPushButton*>(buttons->addButton("Save Key", QDialogButtonBox::AcceptRole));
    buttons->addButton(QDialogButtonBox::Close);
    connect(m_saveBtn, &QPushButton::clicked, this, &SettingsDialog::onSaveKey);
    connect(buttons, &QDialogButtonBox::rejected, this, &QDialog::reject);
    layout->addWidget(buttons);

    connect(m_grpc, &GrpcClient::nexusAPIKeySet, this, &SettingsDialog::onKeyValidated);
    connect(m_grpc, &GrpcClient::rpcError, this, [this](const QString& method, const QString& error) {
        if (method == "SetNexusAPIKey") {
            m_statusLabel->setText(QString("<b style='color: red;'>Error: %1</b>").arg(error));
            m_saveBtn->setEnabled(true);
        }
    });
}

void SettingsDialog::onSaveKey()
{
    QString key = m_apiKeyEdit->text().trimmed();
    if (key.isEmpty()) {
        m_statusLabel->setText("Please enter an API key.");
        return;
    }
    if (!m_grpc->isConnected()) {
        m_statusLabel->setText("<b style='color: red;'>Daemon not connected.</b>");
        return;
    }
    m_statusLabel->setText("Validating...");
    m_saveBtn->setEnabled(false);
    m_grpc->setNexusAPIKey(key);
}

void SettingsDialog::onKeyValidated(bool valid, const QString& errorMessage)
{
    m_saveBtn->setEnabled(true);
    if (valid) {
        m_statusLabel->setText("<b style='color: green;'>Validated!</b>");
    } else {
        m_statusLabel->setText(
            QString("<b style='color: red;'>Invalid: %1</b>").arg(errorMessage));
    }
}

void SettingsDialog::populateProtonCombo()
{
    m_protonCombo->clear();
    m_protonCombo->addItem("Auto (prefer newest: Proton 11 > 10 > 9 > Experimental > Hotfix)",
                           QString());

    if (!m_grpc->isConnected())
        return;

    std::vector<GrpcProtonVersion> versions;
    QString err;
    if (!m_grpc->detectProtonVersions(versions, err)) {
        m_protonStatus->setText(
            QString("<span style='color:#c00;'>Cannot detect Proton: %1</span>").arg(err));
        return;
    }
    for (const auto& v : versions)
        m_protonCombo->addItem(v.name, v.path);

    QString current;
    if (m_grpc->getPreferredProton(current, err) && !current.isEmpty()) {
        int idx = m_protonCombo->findData(current);
        if (idx >= 0)
            m_protonCombo->setCurrentIndex(idx);
    }
}

namespace {

// Locates gorganizer.sh next to the running frontend binary.
QString findGorganizerScript()
{
    QString appDir = QCoreApplication::applicationDirPath();
    QStringList candidates = {
        appDir + "/gorganizer.sh",
        appDir + "/../../gorganizer.sh",
    };
    QByteArray root = qgetenv("GORGANIZER_ROOT");
    if (!root.isEmpty())
        candidates.prepend(QString::fromUtf8(root) + "/gorganizer.sh");
    for (const auto& c : candidates) {
        QFileInfo fi(c);
        if (fi.exists())
            return fi.canonicalFilePath();
    }
    return {};
}

QString xdgConfigHome()
{
    QByteArray v = qgetenv("XDG_CONFIG_HOME");
    if (!v.isEmpty())
        return QString::fromUtf8(v);
    return QDir::homePath() + "/.config";
}

QString xdgDataHome()
{
    QByteArray v = qgetenv("XDG_DATA_HOME");
    if (!v.isEmpty())
        return QString::fromUtf8(v);
    return QDir::homePath() + "/.local/share";
}

} // namespace

void SettingsDialog::onTestNxm()
{
    QStringList rows;
    auto pass = [&](const QString& label) { rows << QString("<span style='color:#080;'>&#10003;</span> %1").arg(label); };
    auto fail = [&](const QString& label) { rows << QString("<span style='color:#c00;'>&#10007;</span> %1").arg(label); };
    auto warn = [&](const QString& label) { rows << QString("<span style='color:#a60;'>&#9888;</span> %1").arg(label); };

    const QString desktopId = "gorganizer-nxm.desktop";
    const QString desktopFile = xdgDataHome() + "/applications/" + desktopId;
    const QString mimeapps = xdgConfigHome() + "/mimeapps.list";
    const QString script = findGorganizerScript();

    QProcess p;
    p.start("xdg-mime", {"query", "default", "x-scheme-handler/nxm"});
    if (p.waitForFinished(3000)) {
        const QString got = QString::fromUtf8(p.readAllStandardOutput()).trimmed();
        if (got == desktopId)
            pass(QString("xdg-mime default = <code>%1</code>").arg(got));
        else if (got.isEmpty())
            fail("xdg-mime returned no default for x-scheme-handler/nxm");
        else
            fail(QString("xdg-mime default = <code>%1</code> (expected <code>%2</code>)").arg(got, desktopId));
    } else {
        warn("xdg-mime not available — skipping query check");
    }

    QFile mf(mimeapps);
    if (mf.open(QIODevice::ReadOnly | QIODevice::Text)) {
        const QString contents = QString::fromUtf8(mf.readAll());
        if (contents.contains(QString("x-scheme-handler/nxm=%1").arg(desktopId)))
            pass(QString("<code>%1</code> contains nxm entry").arg(mimeapps));
        else
            fail(QString("<code>%1</code> missing nxm entry").arg(mimeapps));
    } else {
        fail(QString("<code>%1</code> not readable").arg(mimeapps));
    }

    QFile df(desktopFile);
    if (df.open(QIODevice::ReadOnly | QIODevice::Text)) {
        const QString contents = QString::fromUtf8(df.readAll());
        QString execLine;
        for (const auto& line : contents.split('\n')) {
            if (line.startsWith("Exec=")) {
                execLine = line.mid(5);
                break;
            }
        }
        if (execLine.isEmpty()) {
            fail(QString("<code>%1</code> has no Exec= line").arg(desktopFile));
        } else if (!script.isEmpty() && !execLine.contains(script)) {
            fail(QString("Exec= points elsewhere: <code>%1</code><br>"
                         "&nbsp;&nbsp;Expected to contain: <code>%2</code>")
                     .arg(execLine.toHtmlEscaped(), script.toHtmlEscaped()));
        } else {
            pass(QString("Exec= = <code>%1</code>").arg(execLine.toHtmlEscaped()));
        }
    } else {
        fail(QString("<code>%1</code> missing").arg(desktopFile));
    }

    if (script.isEmpty()) {
        fail("gorganizer.sh not found next to frontend binary");
    } else {
        QFileInfo fi(script);
        if (fi.isExecutable())
            pass(QString("<code>%1</code> is executable").arg(script));
        else
            fail(QString("<code>%1</code> exists but is not executable").arg(script));
    }

    m_nxmStatus->setText(rows.join("<br>"));
}

void SettingsDialog::onReregisterNxm()
{
    const QString script = findGorganizerScript();
    if (script.isEmpty()) {
        m_nxmStatus->setText("<span style='color:#c00;'>Cannot find gorganizer.sh next to the frontend binary.</span>");
        return;
    }

    QProcess p;
    p.start(script, {"--register-nxm"});
    if (!p.waitForFinished(15000)) {
        m_nxmStatus->setText("<span style='color:#c00;'>Re-registration timed out.</span>");
        return;
    }
    if (p.exitStatus() != QProcess::NormalExit || p.exitCode() != 0) {
        m_nxmStatus->setText(QString("<span style='color:#c00;'>Re-registration failed: %1</span>")
                                 .arg(QString::fromUtf8(p.readAllStandardError()).toHtmlEscaped()));
        return;
    }
    m_nxmStatus->setText("<span style='color:#080;'>&#10003; Re-registered. Run 'Test NXM Handler' to verify.</span>");
}

void SettingsDialog::populateThemeCombo()
{
    m_themeCombo->blockSignals(true);
    m_themeCombo->clear();
    m_themeCombo->addItems(ThemeManager::availableThemes());
    QString current = m_config ? m_config->preferredStyle() : QString();
    if (current.isEmpty() || current == "Default")
        current = "Light";
    int idx = m_themeCombo->findText(current);
    if (idx >= 0)
        m_themeCombo->setCurrentIndex(idx);
    m_themeCombo->blockSignals(false);
}

void SettingsDialog::onThemeChanged(const QString& name)
{
    if (!m_config)
        return;
    if (name == "Light") {
        m_config->setAppearanceMode("light");
    } else if (ThemeManager::isDarkVariant(name)) {
        m_config->setPreferredStyle(name);
        m_config->setAppearanceMode("dark");
    }
    ThemeManager::applyMode(m_config->appearanceMode(), m_config->preferredStyle());
}

void SettingsDialog::onSaveProton()
{
    if (!m_grpc->isConnected()) {
        m_protonStatus->setText("<b style='color:#c00;'>Daemon not connected.</b>");
        return;
    }
    QString path = m_protonCombo->currentData().toString();
    QString err;
    if (!m_grpc->setPreferredProton(path, err)) {
        m_protonStatus->setText(
            QString("<b style='color:#c00;'>Save failed: %1</b>").arg(err));
        return;
    }
    m_protonStatus->setText("<b style='color:#080;'>Saved.</b>");
}

void SettingsDialog::onCollapsedSeparatorViewToggled(bool on)
{
    if (m_config)
        m_config->setCollapsedSeparatorView(on);
    emit collapsedSeparatorViewChanged(on);
}

} // namespace gorganizer
