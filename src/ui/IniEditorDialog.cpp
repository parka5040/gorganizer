#include "IniEditorDialog.h"
#include "ThemeManager.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QTabWidget>
#include <QPlainTextEdit>
#include <QCheckBox>
#include <QLabel>
#include <QPushButton>
#include <QFontDatabase>
#include <QMessageBox>
#include <QDialogButtonBox>
#include <QScrollArea>
#include <QFrame>
#include <QFormLayout>
#include <QComboBox>
#include <QSpinBox>
#include <QLineEdit>
#include <QShortcut>
#include <QKeySequence>
#include <QRegularExpression>

namespace gorganizer {

namespace {
// Status-text hues from the active theme so they read in both light and dark.
QString okHex() { return ThemeManager::currentPalette().successFg.name(); }
QString errHex() { return ThemeManager::currentPalette().errorFg.name(); }
} // namespace

IniEditorDialog::IniEditorDialog(GrpcClient* grpc,
                                 const QString& gameId,
                                 const QString& gameDisplayName,
                                 const QString& profileName,
                                 QWidget* parent)
    : QDialog(parent)
    , m_grpc(grpc)
    , m_gameId(gameId)
    , m_profileName(profileName)
    , m_tabs(new QTabWidget)
    , m_enabledCheck(new QCheckBox("Use profile-specific INI files at launch"))
    , m_pathLabel(new QLabel)
    , m_statusLabel(new QLabel)
    , m_saveBtn(new QPushButton("Save"))
{
    setWindowTitle(QString("INI Editor — %1 / %2").arg(gameDisplayName, profileName));
    resize(900, 640);

    auto* layout = new QVBoxLayout(this);

    m_enabledCheck->setToolTip(
        "When on, this profile's INI files are copied into the game's\n"
        "Documents/My Games directory every time the game launches.");
    connect(m_enabledCheck, &QCheckBox::toggled, this, &IniEditorDialog::onToggleEnabled);
    layout->addWidget(m_enabledCheck);

    m_pathLabel->setWordWrap(true);
    m_pathLabel->setObjectName("monoHintLabel");
    m_pathLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
    layout->addWidget(m_pathLabel);

    m_tabs->setTabPosition(QTabWidget::North);
    layout->addWidget(m_tabs, 1);
    connect(m_tabs, &QTabWidget::currentChanged, this, &IniEditorDialog::onTabChanged);

    buildFindBar(layout);

    m_statusLabel->setObjectName("hintLabel");
    layout->addWidget(m_statusLabel);

    auto* findSc = new QShortcut(QKeySequence::Find, this);
    connect(findSc, &QShortcut::activated, this, &IniEditorDialog::onFindShortcut);

    auto* buttons = new QHBoxLayout;
    auto* applyBtn = new QPushButton("Apply Now");
    applyBtn->setToolTip("Copy all INIs to the game's Documents folder immediately.");
    connect(applyBtn, &QPushButton::clicked, this, &IniEditorDialog::onApplyNow);
    buttons->addWidget(applyBtn);
    buttons->addStretch();
    connect(m_saveBtn, &QPushButton::clicked, this, &IniEditorDialog::onSave);
    buttons->addWidget(m_saveBtn);
    auto* closeBtn = new QPushButton("Close");
    connect(closeBtn, &QPushButton::clicked, this, &QDialog::accept);
    buttons->addWidget(closeBtn);
    layout->addLayout(buttons);

    reload();
}

void IniEditorDialog::reload()
{
    m_handles.clear();
    while (m_tabs->count() > 0) {
        QWidget* w = m_tabs->widget(0);
        m_tabs->removeTab(0);
        delete w;
    }

    std::vector<GrpcProfileIniFile> files;
    GrpcProfileIniStatus status;
    QString err;
    if (!m_grpc->listProfileIniFiles(m_gameId, m_profileName, files, status, err)) {
        m_statusLabel->setText(QString("<span style='color:%1;'>Error: %2</span>").arg(errHex(), err));
        m_pathLabel->clear();
        return;
    }

    m_suppressEnabledSignal = true;
    m_enabledCheck->setChecked(status.useCustomIni);
    m_enabledCheck->setEnabled(status.gameSupportsIni || !files.empty());
    m_suppressEnabledSignal = false;

    if (!status.myGamesDir.isEmpty())
        m_pathLabel->setText("Target: " + status.myGamesDir);
    else
        m_pathLabel->setText("No Documents/My Games path detected yet — launch the game once to create it.");

    if (files.empty()) {
        auto* placeholder = new QLabel(
            "No INI files managed for this game.\n"
            "(Either the game isn't in the supported list or no spec was found.)");
        placeholder->setAlignment(Qt::AlignCenter);
        placeholder->setObjectName("hintLabel");
        m_tabs->addTab(placeholder, "—");
        m_saveBtn->setEnabled(false);
        return;
    }

    buildTweaksTab();
    buildResolutionTab(files);

    auto monoFont = QFontDatabase::systemFont(QFontDatabase::FixedFont);
    for (const auto& f : files) {
        TabHandle h;
        h.filename = f.filename;
        h.diskPath = f.diskPath;
        h.originalContent = f.content;
        h.editor = new QPlainTextEdit;
        h.editor->setFont(monoFont);
        h.editor->setTabStopDistance(32);
        h.editor->setPlainText(f.content);
        int handleIdx = m_handles.size();
        m_tabs->addTab(h.editor, f.filename);
        m_handles.append(h);

        connect(h.editor, &QPlainTextEdit::textChanged, this, [this, handleIdx] {
            markDirty(handleIdx, m_handles[handleIdx].editor->toPlainText()
                                     != m_handles[handleIdx].originalContent);
        });
    }

    m_saveBtn->setEnabled(false);
    onTabChanged(m_tabs->currentIndex());
}

void IniEditorDialog::buildTweaksTab()
{
    std::vector<GrpcIniTweakState> tweaks;
    QString err;
    if (!m_grpc->listIniTweaks(m_gameId, m_profileName, tweaks, err)) {
        return;
    }
    if (tweaks.empty())
        return;

    auto* container = new QWidget;
    auto* outer = new QVBoxLayout(container);
    outer->setContentsMargins(6, 6, 6, 6);

    auto* intro = new QLabel(
        "Presets below write into the profile's Custom.ini. "
        "Older engines (Oblivion, Fallout 3/NV, Skyrim LE) don't read Custom.ini "
        "natively — gorganizer merges it into the primary INI when the profile's "
        "custom-INI toggle is on.");
    intro->setWordWrap(true);
    intro->setObjectName("hintLabel");
    outer->addWidget(intro);

    auto* scroll = new QScrollArea;
    scroll->setWidgetResizable(true);
    scroll->setFrameShape(QFrame::NoFrame);
    auto* inner = new QWidget;
    auto* innerLayout = new QVBoxLayout(inner);
    innerLayout->setSpacing(10);

    for (const auto& t : tweaks) {
        auto* box = new QFrame;
        box->setFrameShape(QFrame::StyledPanel);
        auto* boxLayout = new QVBoxLayout(box);
        auto* cb = new QCheckBox(t.name);
        cb->setChecked(t.enabled);
        QFont bold = cb->font();
        bold.setBold(true);
        cb->setFont(bold);
        boxLayout->addWidget(cb);
        if (!t.description.isEmpty()) {
            auto* desc = new QLabel(t.description);
            desc->setWordWrap(true);
            desc->setObjectName("hintLabel");
            desc->setContentsMargins(22, 0, 0, 0);
            boxLayout->addWidget(desc);
        }
        if (!t.targetFile.isEmpty()) {
            auto* target = new QLabel(QString("Writes to: %1").arg(t.targetFile));
            target->setObjectName("monoHintLabel");
            target->setContentsMargins(22, 0, 0, 0);
            boxLayout->addWidget(target);
        }
        QString tweakId = t.id;
        connect(cb, &QCheckBox::toggled, this, [this, tweakId](bool checked) {
            onTweakToggled(tweakId, checked);
        });
        innerLayout->addWidget(box);
    }
    innerLayout->addStretch();
    scroll->setWidget(inner);
    outer->addWidget(scroll, 1);

    m_tweaksTab = container;
    m_tweaksTabIndex = m_tabs->count();
    m_tabs->addTab(container, "Tweaks");
}

void IniEditorDialog::onTweakToggled(const QString& tweakId, bool enabled)
{
    GrpcIniTweakState state;
    QString err;
    if (!m_grpc->setIniTweak(m_gameId, m_profileName, tweakId, enabled, state, err)) {
        QMessageBox::warning(this, "Tweak Failed", err);
        return;
    }
    m_statusLabel->setText(QString("<span style='color:%1;'>%2 %3 %4.</span>")
        .arg(okHex())
        .arg(state.name)
        .arg(state.enabled ? "enabled" : "disabled")
        .arg("(" + state.targetFile + ")"));

    for (int i = 0; i < m_handles.size(); ++i) {
        if (m_handles[i].filename == state.targetFile) {
            std::vector<GrpcProfileIniFile> files;
            GrpcProfileIniStatus st;
            QString lerr;
            if (m_grpc->listProfileIniFiles(m_gameId, m_profileName, files, st, lerr)) {
                for (const auto& f : files) {
                    if (f.filename == state.targetFile) {
                        int prefix = 0;
                        if (m_tweaksTabIndex >= 0) ++prefix;
                        if (m_resolutionTabIndex >= 0) ++prefix;
                        m_handles[i].editor->blockSignals(true);
                        m_handles[i].editor->setPlainText(f.content);
                        m_handles[i].editor->blockSignals(false);
                        m_handles[i].originalContent = f.content;
                        m_tabs->setTabText(i + prefix, f.filename);
                        break;
                    }
                }
            }
            break;
        }
    }
}

void IniEditorDialog::onTabChanged(int index)
{
    if (index == m_tweaksTabIndex || index == m_resolutionTabIndex) {
        m_statusLabel->clear();
        return;
    }
    int prefix = 0;
    if (m_tweaksTabIndex >= 0) ++prefix;
    if (m_resolutionTabIndex >= 0) ++prefix;
    int handleIdx = index - prefix;
    if (handleIdx < 0 || handleIdx >= m_handles.size())
        return;
    m_statusLabel->setText("File on disk: " + m_handles[handleIdx].diskPath);
}

void IniEditorDialog::markDirty(int handleIndex, bool dirty)
{
    if (handleIndex < 0 || handleIndex >= m_handles.size())
        return;
    QString label = m_handles[handleIndex].filename;
    if (dirty)
        label += " *";
    int prefix = 0;
    if (m_tweaksTabIndex >= 0) ++prefix;
    if (m_resolutionTabIndex >= 0) ++prefix;
    m_tabs->setTabText(handleIndex + prefix, label);
    m_saveBtn->setEnabled(anyDirty());
}

bool IniEditorDialog::anyDirty() const
{
    for (int i = 0; i < m_handles.size(); ++i) {
        if (m_handles[i].editor->toPlainText() != m_handles[i].originalContent)
            return true;
    }
    return false;
}

void IniEditorDialog::onSave()
{
    int prefix = 0;
    if (m_tweaksTabIndex >= 0) ++prefix;
    if (m_resolutionTabIndex >= 0) ++prefix;
    for (int i = 0; i < m_handles.size(); ++i) {
        auto& h = m_handles[i];
        QString current = h.editor->toPlainText();
        if (current == h.originalContent)
            continue;
        QString err;
        if (!m_grpc->saveProfileIniFile(m_gameId, m_profileName, h.filename, current, err)) {
            QMessageBox::warning(this, "Save Failed",
                QString("Failed to save %1: %2").arg(h.filename, err));
            return;
        }
        h.originalContent = current;
        m_tabs->setTabText(i + prefix, h.filename);
    }
    m_saveBtn->setEnabled(false);
    m_statusLabel->setText(QString("<span style='color:%1;'>Saved.</span>").arg(okHex()));
}

void IniEditorDialog::onToggleEnabled(bool checked)
{
    if (m_suppressEnabledSignal)
        return;
    GrpcProfileIniStatus status;
    QString err;
    if (!m_grpc->setProfileIniEnabled(m_gameId, m_profileName, checked, status, err)) {
        QMessageBox::warning(this, "Error", err);
        m_suppressEnabledSignal = true;
        m_enabledCheck->setChecked(!checked);
        m_suppressEnabledSignal = false;
        return;
    }
    m_statusLabel->setText(checked
        ? "Custom INI is active. INIs will be pushed at game launch."
        : "Custom INI is off. The game will read its own INI files.");
}

void IniEditorDialog::onApplyNow()
{
    if (anyDirty()) {
        auto reply = QMessageBox::question(this, "Unsaved Edits",
            "You have unsaved edits. Save before applying?",
            QMessageBox::Save | QMessageBox::Cancel);
        if (reply == QMessageBox::Cancel)
            return;
        onSave();
    }
    GrpcProfileIniStatus status;
    QString err;
    if (!m_grpc->getProfileIniStatus(m_gameId, m_profileName, status, err)) {
        QMessageBox::warning(this, "Error", err);
        return;
    }
    if (status.useCustomIni) {
        if (!m_handles.isEmpty()) {
            const auto& h = m_handles.first();
            m_grpc->saveProfileIniFile(m_gameId, m_profileName, h.filename, h.originalContent, err);
        }
        m_statusLabel->setText(QString("<span style='color:%1;'>Applied to %2</span>").arg(okHex(), status.myGamesDir));
        return;
    }
    m_grpc->setProfileIniEnabled(m_gameId, m_profileName, true, status, err);
    if (!m_handles.isEmpty()) {
        const auto& h = m_handles.first();
        m_grpc->saveProfileIniFile(m_gameId, m_profileName, h.filename, h.originalContent, err);
    }
    m_grpc->setProfileIniEnabled(m_gameId, m_profileName, false, status, err);
    m_statusLabel->setText(QString("<span style='color:%1;'>Pushed one-shot. Toggle \"Use profile-specific INI\" to make it persistent.</span>").arg(okHex()));
}

namespace {

struct Resolution { int w; int h; const char* label; };
const QVector<Resolution>& commonResolutions()
{
    static const QVector<Resolution> list = {
        {1280, 720,  "1280 × 720 (HD / 720p)"},
        {1366, 768,  "1366 × 768 (laptop)"},
        {1600, 900,  "1600 × 900"},
        {1920, 1080, "1920 × 1080 (FHD / 1080p)"},
        {1920, 1200, "1920 × 1200 (WUXGA)"},
        {2560, 1080, "2560 × 1080 (ultrawide)"},
        {2560, 1440, "2560 × 1440 (QHD / 1440p)"},
        {3440, 1440, "3440 × 1440 (ultrawide QHD)"},
        {3840, 2160, "3840 × 2160 (4K UHD)"},
    };
    return list;
}

// Line-preserving patch of one section.key=value, creating the section if missing.
QString patchIniSectionKey(const QString& original, const QString& section,
                            const QString& key, const QString& value)
{
    QStringList lines = original.split('\n');
    QRegularExpression sectionRe(QString(R"(^\s*\[%1\]\s*$)").arg(section),
                                  QRegularExpression::CaseInsensitiveOption);
    QRegularExpression keyRe(QString(R"(^\s*%1\s*=)").arg(QRegularExpression::escape(key)),
                              QRegularExpression::CaseInsensitiveOption);

    int sectionIdx = -1;
    for (int i = 0; i < lines.size(); ++i) {
        if (sectionRe.match(lines[i]).hasMatch()) {
            sectionIdx = i;
            break;
        }
    }
    QString newLine = QString("%1=%2").arg(key, value);
    if (sectionIdx < 0) {
        if (!lines.isEmpty() && !lines.last().isEmpty())
            lines.append("");
        lines.append(QString("[%1]").arg(section));
        lines.append(newLine);
        return lines.join('\n');
    }
    int insertAt = lines.size();
    for (int i = sectionIdx + 1; i < lines.size(); ++i) {
        QString s = lines[i].trimmed();
        if (s.startsWith('[') && s.endsWith(']')) {
            insertAt = i;
            break;
        }
        if (keyRe.match(lines[i]).hasMatch()) {
            lines[i] = newLine;
            return lines.join('\n');
        }
    }
    lines.insert(insertAt, newLine);
    return lines.join('\n');
}

} // anonymous namespace

void IniEditorDialog::buildResolutionTab(const std::vector<GrpcProfileIniFile>& files)
{
    if (files.empty())
        return;

    auto* container = new QWidget;
    auto* outer = new QVBoxLayout(container);
    outer->setContentsMargins(8, 8, 8, 8);
    outer->setSpacing(10);

    auto* intro = new QLabel(
        "Sets <b>iWidth</b> and <b>iHeight</b> in <b>[Display]</b> of the "
        "chosen INI. Pick a common screen resolution or type custom values.");
    intro->setWordWrap(true);
    intro->setObjectName("hintLabel");
    outer->addWidget(intro);

    auto* form = new QFormLayout;

    m_resolutionPreset = new QComboBox;
    m_resolutionPreset->addItem("Custom...", QVariant::fromValue<int>(-1));
    for (int i = 0; i < commonResolutions().size(); ++i) {
        const auto& r = commonResolutions()[i];
        m_resolutionPreset->addItem(QString::fromLatin1(r.label), i);
    }
    form->addRow("Preset:", m_resolutionPreset);

    m_resolutionWidth = new QSpinBox;
    m_resolutionWidth->setRange(320, 15360);
    m_resolutionWidth->setValue(1920);
    m_resolutionWidth->setSuffix(" px");
    form->addRow("iWidth:", m_resolutionWidth);

    m_resolutionHeight = new QSpinBox;
    m_resolutionHeight->setRange(240, 8640);
    m_resolutionHeight->setValue(1080);
    m_resolutionHeight->setSuffix(" px");
    form->addRow("iHeight:", m_resolutionHeight);

    m_resolutionTarget = new QComboBox;
    QString defaultTarget;
    for (const auto& f : files) {
        m_resolutionTarget->addItem(f.filename);
        if (f.filename.endsWith("Prefs.ini", Qt::CaseInsensitive))
            defaultTarget = f.filename;
    }
    if (!defaultTarget.isEmpty())
        m_resolutionTarget->setCurrentText(defaultTarget);
    form->addRow("Target file:", m_resolutionTarget);

    outer->addLayout(form);

    connect(m_resolutionPreset, &QComboBox::currentIndexChanged, this, [this](int) {
        int idx = m_resolutionPreset->currentData().toInt();
        if (idx < 0) return;
        const auto& r = commonResolutions()[idx];
        m_resolutionWidth->setValue(r.w);
        m_resolutionHeight->setValue(r.h);
    });
    auto syncPreset = [this]() {
        int w = m_resolutionWidth->value();
        int h = m_resolutionHeight->value();
        int match = -1;
        for (int i = 0; i < commonResolutions().size(); ++i) {
            if (commonResolutions()[i].w == w && commonResolutions()[i].h == h) {
                match = i;
                break;
            }
        }
        m_resolutionPreset->blockSignals(true);
        m_resolutionPreset->setCurrentIndex(match < 0 ? 0 : match + 1);
        m_resolutionPreset->blockSignals(false);
    };
    connect(m_resolutionWidth, &QSpinBox::valueChanged, this, syncPreset);
    connect(m_resolutionHeight, &QSpinBox::valueChanged, this, syncPreset);

    auto* btnRow = new QHBoxLayout;
    auto* applyBtn = new QPushButton("Apply to INI");
    applyBtn->setToolTip("Writes the keys into [Display] of the selected INI. Click Save to persist.");
    connect(applyBtn, &QPushButton::clicked, this, &IniEditorDialog::onApplyResolution);
    btnRow->addWidget(applyBtn);
    btnRow->addStretch();
    outer->addLayout(btnRow);

    m_resolutionStatus = new QLabel;
    m_resolutionStatus->setWordWrap(true);
    m_resolutionStatus->setObjectName("hintLabel");
    outer->addWidget(m_resolutionStatus);
    outer->addStretch();

    m_resolutionTab = container;
    m_resolutionTabIndex = m_tabs->count();
    m_tabs->addTab(container, "Resolution");
}

void IniEditorDialog::onApplyResolution()
{
    int w = m_resolutionWidth->value();
    int h = m_resolutionHeight->value();
    QString target = m_resolutionTarget->currentText();
    if (target.isEmpty()) {
        m_resolutionStatus->setText(
            QString("<span style='color:%1;'>No target INI file available.</span>").arg(errHex()));
        return;
    }
    applyResolutionTo(target, w, h);
    m_resolutionStatus->setText(
        QString("<span style='color:%1;'>Staged %2×%3 into [Display] of %4. "
                "Click Save to persist.</span>").arg(okHex()).arg(w).arg(h).arg(target));
}

void IniEditorDialog::applyResolutionTo(const QString& filename, int width, int height)
{
    for (int i = 0; i < m_handles.size(); ++i) {
        if (m_handles[i].filename != filename)
            continue;
        QString content = m_handles[i].editor->toPlainText();
        content = patchIniSectionKey(content, "Display", "iWidth", QString::number(width));
        content = patchIniSectionKey(content, "Display", "iHeight", QString::number(height));
        m_handles[i].editor->setPlainText(content);
        return;
    }
    std::vector<GrpcProfileIniFile> files;
    GrpcProfileIniStatus st;
    QString err;
    if (!m_grpc->listProfileIniFiles(m_gameId, m_profileName, files, st, err))
        return;
    for (const auto& f : files) {
        if (f.filename != filename) continue;
        QString content = f.content;
        content = patchIniSectionKey(content, "Display", "iWidth", QString::number(width));
        content = patchIniSectionKey(content, "Display", "iHeight", QString::number(height));
        m_grpc->saveProfileIniFile(m_gameId, m_profileName, filename, content, err);
        return;
    }
}

void IniEditorDialog::buildFindBar(QVBoxLayout* parentLayout)
{
    m_findBar = new QWidget;
    auto* row = new QHBoxLayout(m_findBar);
    row->setContentsMargins(4, 2, 4, 2);
    auto* label = new QLabel("Find:");
    row->addWidget(label);
    m_findInput = new QLineEdit;
    m_findInput->setPlaceholderText("Search the current INI…");
    row->addWidget(m_findInput, 1);
    auto* nextBtn = new QPushButton("Next");
    row->addWidget(nextBtn);
    auto* closeBtn = new QPushButton("×");
    closeBtn->setFixedWidth(28);
    closeBtn->setToolTip("Close (Esc)");
    row->addWidget(closeBtn);
    m_findStatus = new QLabel;
    m_findStatus->setObjectName("hintLabel");
    row->addWidget(m_findStatus);

    connect(m_findInput, &QLineEdit::returnPressed, this, &IniEditorDialog::onFindNext);
    connect(nextBtn, &QPushButton::clicked, this, &IniEditorDialog::onFindNext);
    connect(closeBtn, &QPushButton::clicked, this, &IniEditorDialog::onFindClose);

    auto* esc = new QShortcut(QKeySequence(Qt::Key_Escape), m_findInput);
    esc->setContext(Qt::WidgetShortcut);
    connect(esc, &QShortcut::activated, this, &IniEditorDialog::onFindClose);

    m_findBar->hide();
    parentLayout->addWidget(m_findBar);
}

QPlainTextEdit* IniEditorDialog::currentEditor() const
{
    int idx = m_tabs->currentIndex();
    int prefix = 0;
    if (m_tweaksTabIndex >= 0) ++prefix;
    if (m_resolutionTabIndex >= 0) ++prefix;
    int handleIdx = idx - prefix;
    if (handleIdx < 0 || handleIdx >= m_handles.size())
        return nullptr;
    return m_handles[handleIdx].editor;
}

void IniEditorDialog::onFindShortcut()
{
    if (!m_findBar) return;
    m_findBar->show();
    m_findInput->setFocus();
    m_findInput->selectAll();
    m_findStatus->clear();
}

void IniEditorDialog::onFindNext()
{
    auto* editor = currentEditor();
    if (!editor) {
        m_findStatus->setText(QString("<span style='color:%1;'>Open an INI tab first.</span>").arg(errHex()));
        return;
    }
    QString needle = m_findInput->text();
    if (needle.isEmpty()) return;
    bool found = editor->find(needle);
    if (!found) {
        QTextCursor c = editor->textCursor();
        c.movePosition(QTextCursor::Start);
        editor->setTextCursor(c);
        found = editor->find(needle);
    }
    m_findStatus->setText(found
        ? QString("<span style='color:%1;'>match</span>").arg(okHex())
        : QString("<span style='color:%1;'>not found</span>").arg(errHex()));
}

void IniEditorDialog::onFindClose()
{
    if (!m_findBar) return;
    m_findBar->hide();
    auto* editor = currentEditor();
    if (editor) editor->setFocus();
}

} // namespace gorganizer
