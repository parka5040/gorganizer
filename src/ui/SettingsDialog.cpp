#include "SettingsDialog.h"
#include "AppConfig.h"
#include "GrpcClient.h"
#include "ErrorPresenter.h"
#include "ThemeManager.h"
#include "WindowFit.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QFormLayout>
#include <QScrollArea>
#include <QLineEdit>
#include <QDialogButtonBox>
#include <QLabel>
#include <QPushButton>
#include <QComboBox>
#include <QCheckBox>
#include <QProcess>
#include <QTimer>
#include <QFileInfo>
#include <QFile>
#include <QDir>
#include <QStandardPaths>
#include <QCoreApplication>
#include <unistd.h>

namespace gorganizer {

namespace {
QString okHex() { return ThemeManager::currentPalette().successFg.name(); }
QString errHex() { return ThemeManager::currentPalette().errorFg.name(); }
QString warnHex() { return ThemeManager::currentPalette().warningFg.name(); }
}

SettingsDialog::SettingsDialog(GrpcClient* grpc, AppConfig* config, QWidget* parent)
    : QDialog(parent)
    , m_grpc(grpc)
    , m_config(config)
{
    setWindowTitle("Settings");

    auto* layout = new QVBoxLayout(this);
    auto* scroll = new QScrollArea;
    scroll->setWidgetResizable(true);
    scroll->setFrameShape(QFrame::NoFrame);
    auto* content = new QWidget;
    auto* form = new QFormLayout(content);

    m_themeCombo = new QComboBox;
    populateThemeCombo();
    connect(m_themeCombo, &QComboBox::currentTextChanged, this, &SettingsDialog::onThemeChanged);
    form->addRow("Theme:", m_themeCombo);

    m_collapseViewsCheck = new QCheckBox("Show one ordering for both views");
    m_collapseViewsCheck->setToolTip(
        "Keep grouped and priority views in the same order. Turning this off does not restore the previous order.");
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
    m_protonCombo->setMinimumWidth(140);
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
    QString socketPath = xdg && xdg[0]
        ? QString::fromUtf8(xdg) + "/gorganizer/gorganizer.sock"
        : QDir::tempPath() + "/gorganizer-" + QString::number(getuid()) + "/gorganizer.sock";
    socketLabel->setText(socketPath);
    socketLabel->setWordWrap(true);
    socketLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
    form->addRow("Daemon Socket:", socketLabel);

    auto* nxmRow = new QHBoxLayout;
    auto* testNxmBtn = new QPushButton("Test NXM Handler");
    m_reregNxmBtn = new QPushButton("Re-register");
    nxmRow->addWidget(testNxmBtn);
    nxmRow->addWidget(m_reregNxmBtn);
    nxmRow->addStretch();
    connect(testNxmBtn, &QPushButton::clicked, this, &SettingsDialog::onTestNxm);
    connect(m_reregNxmBtn, &QPushButton::clicked, this, &SettingsDialog::onReregisterNxm);
    form->addRow("Nexus NXM Handler:", nxmRow);

    m_nxmStatus = new QLabel;
    m_nxmStatus->setTextFormat(Qt::RichText);
    m_nxmStatus->setWordWrap(true);
    form->addRow("", m_nxmStatus);

    scroll->setWidget(content);
    layout->addWidget(scroll, 1);

    populateProtonCombo();

    auto* buttons = new QDialogButtonBox;
    m_saveBtn = static_cast<QPushButton*>(buttons->addButton("Save Key", QDialogButtonBox::AcceptRole));
    buttons->addButton(QDialogButtonBox::Close);
    connect(m_saveBtn, &QPushButton::clicked, this, &SettingsDialog::onSaveKey);
    connect(buttons, &QDialogButtonBox::rejected, this, &QDialog::reject);
    layout->addWidget(buttons);
    buttons->button(QDialogButtonBox::Close)->setDefault(true);
    fitToScreen(this, QSize(560, 520));

    connect(m_grpc, &GrpcClient::nexusAPIKeySet, this, &SettingsDialog::onKeyValidated);
    connect(m_grpc, &GrpcClient::rpcError, this, [this](const QString& method, const QString& error, int grpcCode) {
        if (method == "SetNexusAPIKey") {
            const GrpcError failure{grpcCode, method, error};
            m_statusLabel->setText(QString("<b style='color:%1;'>%2</b>")
                                       .arg(errHex(), errorSummary("save the Nexus API key", failure, true).toHtmlEscaped()));
            presentError(this, "API Key Not Saved", "save the Nexus API key", failure, true);
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
        m_statusLabel->setText(QString("<b style='color:%1;'>Daemon not connected.</b>").arg(errHex()));
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
        m_statusLabel->setText(QString("<b style='color:%1;'>Validated!</b>").arg(okHex()));
    } else {
        m_statusLabel->setText(
            QString("<b style='color:%1;'>%2</b>")
                .arg(errHex(), errorSummary("validate the Nexus API key", errorMessage).toHtmlEscaped()));
        presentError(this, "API Key Not Validated", "validate the Nexus API key", errorMessage);
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
    GrpcError err;
    if (!m_grpc->detectProtonVersions(versions, err)) {
        m_protonStatus->setText(
            QString("<span style='color:%1;'>%2</span>")
                .arg(errHex(), errorSummary("detect Proton versions", err, false).toHtmlEscaped()));
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

}

void SettingsDialog::onTestNxm()
{
    QStringList rows;
    auto pass = [&](const QString& label) { rows << QString("<span style='color:%1;'>&#10003;</span> %2").arg(okHex(), label); };
    auto fail = [&](const QString& label) { rows << QString("<span style='color:%1;'>&#10007;</span> %2").arg(errHex(), label); };
    auto warn = [&](const QString& label) { rows << QString("<span style='color:%1;'>&#9888;</span> %2").arg(warnHex(), label); };

    const QString desktopId = "gorganizer-nxm.desktop";
    const QString desktopFile = xdgDataHome() + "/applications/" + desktopId;
    const QString mimeapps = xdgConfigHome() + "/mimeapps.list";
    const QString script = findGorganizerScript();
    const QString launcher = xdgDataHome() + "/gorganizer/bin/gorganizer";

    QProcess p;
    p.start("xdg-mime", {"query", "default", "x-scheme-handler/nxm"});
    if (p.waitForFinished(3000)) {
        const QString got = QString::fromUtf8(p.readAllStandardOutput()).trimmed();
        if (got == desktopId)
            pass(QString("xdg-mime default = <code>%1</code>").arg(got.toHtmlEscaped()));
        else if (got.isEmpty())
            fail("xdg-mime returned no default for x-scheme-handler/nxm");
        else
            fail(QString("xdg-mime default = <code>%1</code> (expected <code>%2</code>)").arg(got.toHtmlEscaped(), desktopId.toHtmlEscaped()));
    } else {
        warn("xdg-mime not available — skipping query check");
    }

    QFile mf(mimeapps);
    if (mf.open(QIODevice::ReadOnly | QIODevice::Text)) {
        const QString contents = QString::fromUtf8(mf.readAll());
        if (contents.contains(QString("x-scheme-handler/nxm=%1").arg(desktopId)))
            pass(QString("<code>%1</code> contains nxm entry").arg(mimeapps.toHtmlEscaped()));
        else
            fail(QString("<code>%1</code> missing nxm entry").arg(mimeapps.toHtmlEscaped()));
    } else {
        fail(QString("<code>%1</code> not readable").arg(mimeapps.toHtmlEscaped()));
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
            fail(QString("<code>%1</code> has no Exec= line").arg(desktopFile.toHtmlEscaped()));
        } else if (!execLine.contains("/gorganizer/bin/gorganizer")) {
            fail(QString("The desktop shortcut points somewhere else: <code>%1</code>")
                     .arg(execLine.toHtmlEscaped()));
        } else {
            pass(QString("Exec= = <code>%1</code>").arg(execLine.toHtmlEscaped()));
        }
    } else {
        fail(QString("<code>%1</code> missing").arg(desktopFile.toHtmlEscaped()));
    }

    QFileInfo launcherInfo(launcher);
    if (!launcherInfo.isExecutable())
        fail("The desktop shortcut needs updating. Select Re-register.");

    if (script.isEmpty()) {
        fail("gorganizer.sh not found next to frontend binary");
    } else {
        QFileInfo fi(script);
        if (fi.isExecutable())
            pass(QString("<code>%1</code> is executable").arg(script.toHtmlEscaped()));
        else
            fail(QString("<code>%1</code> exists but is not executable").arg(script.toHtmlEscaped()));
    }

    m_nxmStatus->setText(rows.join("<br>"));
}

void SettingsDialog::onReregisterNxm()
{
    if (m_nxmRegisterProcess)
        return;

    const QString script = findGorganizerScript();
    if (script.isEmpty()) {
        m_nxmStatus->setText(QString("<span style='color:%1;'>Cannot find gorganizer.sh next to the frontend binary.</span>").arg(errHex()));
        return;
    }

    auto* process = new QProcess(this);
    m_nxmRegisterProcess = process;
    m_reregNxmBtn->setEnabled(false);
    connect(process, &QProcess::finished, this,
            [this, process](int exitCode, QProcess::ExitStatus exitStatus) {
                if (m_nxmRegisterProcess != process)
                    return;
                m_nxmRegisterProcess = nullptr;
                m_reregNxmBtn->setEnabled(true);
                if (exitStatus != QProcess::NormalExit || exitCode != 0) {
                    const QString error = QString::fromUtf8(process->readAllStandardError());
                    m_nxmStatus->setText(QString("<span style='color:%1;'>%2</span>")
                                             .arg(errHex(), errorSummary("register Nexus download links", error).toHtmlEscaped()));
                    presentError(this, "Registration Failed", "register Nexus download links", error);
                } else {
                    m_nxmStatus->setText(QString("<span style='color:%1;'>&#10003; Nexus download links are enabled. Use &quot;Test NXM Handler&quot; to check.</span>").arg(okHex()));
                }
                process->deleteLater();
            });
    connect(process, &QProcess::errorOccurred, this,
            [this, process](QProcess::ProcessError) {
                if (m_nxmRegisterProcess != process)
                    return;
                m_nxmRegisterProcess = nullptr;
                m_reregNxmBtn->setEnabled(true);
                QString detail = QString::fromUtf8(process->readAllStandardError());
                if (detail.isEmpty())
                    detail = process->errorString();
                m_nxmStatus->setText(QString("<span style='color:%1;'>%2</span>")
                                         .arg(errHex(), errorSummary("register Nexus download links", detail).toHtmlEscaped()));
                presentError(this, "Registration Failed", "register Nexus download links", detail);
                process->kill();
                process->deleteLater();
            });
    QTimer::singleShot(15000, process, [this, process] {
        if (m_nxmRegisterProcess != process)
            return;
        m_nxmRegisterProcess = nullptr;
        process->kill();
        m_reregNxmBtn->setEnabled(true);
        m_nxmStatus->setText(QString("<span style='color:%1;'>Re-registration timed out.</span>").arg(errHex()));
        process->deleteLater();
    });
    process->start(script, {"register"});
}

void SettingsDialog::populateThemeCombo()
{
    m_themeCombo->blockSignals(true);
    m_themeCombo->clear();
    m_themeCombo->addItems(ThemeManager::availableThemes());
    QString current = ThemeManager::canonicalThemeName(m_config ? m_config->preferredStyle() : QString());
    int idx = m_themeCombo->findText(current);
    if (idx >= 0)
        m_themeCombo->setCurrentIndex(idx);
    m_themeCombo->blockSignals(false);
}

void SettingsDialog::onThemeChanged(const QString& name)
{
    if (!m_config)
        return;
    m_config->setPreferredStyle(name);
    ThemeManager::applyMode(m_config->appearanceMode(), name);
}

void SettingsDialog::onSaveProton()
{
    if (!m_grpc->isConnected()) {
        m_protonStatus->setText(QString("<b style='color:%1;'>Daemon not connected.</b>").arg(errHex()));
        return;
    }
    QString path = m_protonCombo->currentData().toString();
    GrpcError err;
    if (!m_grpc->setPreferredProton(path, err)) {
        m_protonStatus->setText(
            QString("<b style='color:%1;'>%2</b>")
                .arg(errHex(), errorSummary("save the Proton version", err, true).toHtmlEscaped()));
        presentError(this, "Proton Not Saved", "save the Proton version", err, true);
        return;
    }
    m_protonStatus->setText(QString("<b style='color:%1;'>Saved.</b>").arg(okHex()));
}

void SettingsDialog::onCollapsedSeparatorViewToggled(bool on)
{
    if (m_config)
        m_config->setCollapsedSeparatorView(on);
    emit collapsedSeparatorViewChanged(on);
}

}
