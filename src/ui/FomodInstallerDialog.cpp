#include "FomodInstallerDialog.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QStackedWidget>
#include <QLabel>
#include <QTextEdit>
#include <QPushButton>
#include <QCheckBox>
#include <QRadioButton>
#include <QGroupBox>
#include <QButtonGroup>
#include <QScrollArea>
#include <QAbstractButton>
#include <QPixmap>

namespace gorganizer {

namespace {

QString titleFor(const FomodPlan& plan)
{
    QString title = "Install";
    if (!plan.moduleName.isEmpty())
        title = QString("Install %1").arg(plan.moduleName);
    return title;
}

// Initial checked state for a plugin inside a given group type.
bool initialChecked(FomodGroupType type, FomodPluginState state, int indexInGroup, bool firstEligibleTaken)
{
    if (state == FomodPluginState::Required) return true;
    if (state == FomodPluginState::NotUsable) return false;

    switch (type) {
        case FomodGroupType::SelectAll:
            return true;
        case FomodGroupType::SelectExactlyOne: {
            // First Recommended; else first non-NotUsable.
            if (state == FomodPluginState::Recommended) return true;
            if (!firstEligibleTaken && indexInGroup == 0) return true;
            return false;
        }
        case FomodGroupType::SelectAtMostOne:
            return state == FomodPluginState::Recommended;
        case FomodGroupType::SelectAtLeastOne:
        case FomodGroupType::SelectAny:
        default:
            return state == FomodPluginState::Recommended;
    }
}

} // namespace

FomodInstallerDialog::FomodInstallerDialog(const FomodPlan& plan, QWidget* parent)
    : QDialog(parent)
    , m_plan(plan)
    , m_stack(new QStackedWidget)
    , m_backBtn(new QPushButton("< Back"))
    , m_nextBtn(new QPushButton("Next >"))
    , m_cancelBtn(new QPushButton("Cancel"))
    , m_titleLabel(new QLabel)
    , m_descriptionText(new QTextEdit)
{
    setWindowTitle(titleFor(plan));
    resize(780, 560);

    auto* outer = new QVBoxLayout(this);

    m_titleLabel->setStyleSheet("font-weight: bold; font-size: 14pt; padding: 4px;");
    outer->addWidget(m_titleLabel);

    auto* body = new QHBoxLayout;
    body->addWidget(m_stack, 2);
    m_descriptionText->setReadOnly(true);
    m_descriptionText->setMinimumWidth(260);
    body->addWidget(m_descriptionText, 1);
    outer->addLayout(body, 1);

    auto* buttons = new QHBoxLayout;
    buttons->addWidget(m_cancelBtn);
    buttons->addStretch();
    buttons->addWidget(m_backBtn);
    buttons->addWidget(m_nextBtn);
    outer->addLayout(buttons);

    connect(m_cancelBtn, &QPushButton::clicked, this, &QDialog::reject);
    connect(m_backBtn, &QPushButton::clicked, this, &FomodInstallerDialog::onBack);
    connect(m_nextBtn, &QPushButton::clicked, this, &FomodInstallerDialog::onNext);

    buildPages();
    showStep(0);
}

void FomodInstallerDialog::buildPages()
{
    // Legacy NMM-style FOMOD: just a fomod/info.xml, no install steps. Show
    // a single info page (description + screenshot) and treat Accept as a
    // confirmation that we may flat-copy everything outside fomod/. The
    // companion C# script.cs is intentionally not executed.
    if (m_plan.legacyInfoOnly) {
        auto* page = new QWidget;
        auto* layout = new QVBoxLayout(page);
        layout->setContentsMargins(4, 4, 4, 4);

        QStringList meta;
        if (!m_plan.author.isEmpty())   meta << QString("Author: %1").arg(m_plan.author);
        if (!m_plan.version.isEmpty())  meta << QString("Version: %1").arg(m_plan.version);
        if (!meta.isEmpty()) {
            auto* metaLbl = new QLabel(meta.join("    "));
            metaLbl->setStyleSheet("color: gray;");
            layout->addWidget(metaLbl);
        }

        if (!m_plan.screenshotPath.isEmpty()) {
            QPixmap pm(m_plan.screenshotPath);
            if (!pm.isNull()) {
                auto* img = new QLabel;
                img->setPixmap(pm.scaled(QSize(640, 360),
                                         Qt::KeepAspectRatio,
                                         Qt::SmoothTransformation));
                img->setAlignment(Qt::AlignCenter);
                layout->addWidget(img);
            }
        }

        auto* note = new QLabel(
            "This is a legacy NMM-style FOMOD. "
            "Gorganizer will copy every file outside the <code>fomod/</code> directory "
            "into the target mod. Any C# install script is <b>not</b> executed.");
        note->setWordWrap(true);
        note->setTextFormat(Qt::RichText);
        layout->addWidget(note);
        layout->addStretch();

        m_stack->addWidget(page);
        m_stepWidgets.append(StepWidgets{});
        m_descriptionText->setPlainText(m_plan.description);
        return;
    }

    for (int i = 0; i < m_plan.steps.size(); ++i)
        buildStepPage(m_plan.steps[i], i);

    // If the FOMOD had no install steps (only requiredInstallFiles), add a
    // confirmation page so the user still gets to review the mod name.
    if (m_plan.steps.isEmpty()) {
        auto* page = new QWidget;
        auto* layout = new QVBoxLayout(page);
        auto* label = new QLabel(
            "This FOMOD has no optional steps — all files are required and will be installed.");
        label->setWordWrap(true);
        layout->addWidget(label);
        layout->addStretch();
        m_stack->addWidget(page);
        m_stepWidgets.append(StepWidgets{});
    }
}

void FomodInstallerDialog::buildStepPage(const FomodStep& step, int stepIdx)
{
    Q_UNUSED(stepIdx);

    auto* scroll = new QScrollArea;
    scroll->setWidgetResizable(true);
    auto* page = new QWidget;
    auto* layout = new QVBoxLayout(page);
    layout->setContentsMargins(4, 4, 4, 4);

    StepWidgets widgets;

    for (const auto& group : step.groups) {
        auto* box = new QGroupBox(group.name);
        auto* boxLayout = new QVBoxLayout(box);

        QButtonGroup* buttonGroup = nullptr;
        const bool isExclusive = (group.type == FomodGroupType::SelectExactlyOne ||
                                  group.type == FomodGroupType::SelectAtMostOne);
        if (isExclusive) {
            buttonGroup = new QButtonGroup(box);
            buttonGroup->setExclusive(group.type == FomodGroupType::SelectExactlyOne);
        }
        QList<QAbstractButton*> buttons;

        bool firstEligibleTaken = false;
        for (int i = 0; i < group.plugins.size(); ++i) {
            const auto& plugin = group.plugins[i];
            QAbstractButton* btn = nullptr;
            if (isExclusive)
                btn = new QRadioButton(plugin.name);
            else
                btn = new QCheckBox(plugin.name);

            bool checked = initialChecked(group.type, plugin.defaultState, i, firstEligibleTaken);
            if (checked) firstEligibleTaken = true;
            btn->setChecked(checked);

            if (plugin.defaultState == FomodPluginState::Required) {
                btn->setEnabled(false);
                btn->setToolTip("Required");
            } else if (plugin.defaultState == FomodPluginState::NotUsable) {
                btn->setEnabled(false);
                btn->setToolTip("Not usable");
            }
            if (group.type == FomodGroupType::SelectAll) {
                btn->setEnabled(false);
                btn->setToolTip("All options installed");
            }

            connect(btn, &QAbstractButton::toggled, this, [this, plugin] {
                renderDescription(plugin.name, plugin.description);
            });
            connect(btn, &QAbstractButton::clicked, this, [this, plugin] {
                renderDescription(plugin.name, plugin.description);
            });

            if (buttonGroup)
                buttonGroup->addButton(btn, i);
            boxLayout->addWidget(btn);
            buttons.append(btn);
        }

        widgets.groupButtons.append(buttonGroup);
        widgets.pluginButtons.append(buttons);
        layout->addWidget(box);
    }

    layout->addStretch();
    scroll->setWidget(page);
    m_stack->addWidget(scroll);
    m_stepWidgets.append(widgets);
}

void FomodInstallerDialog::showStep(int idx)
{
    if (idx < 0 || idx >= m_stack->count()) return;
    m_currentStep = idx;
    m_stack->setCurrentIndex(idx);
    QString stepName;
    if (idx < m_plan.steps.size())
        stepName = m_plan.steps[idx].name;
    m_titleLabel->setText(stepName.isEmpty() ? titleFor(m_plan) : stepName);
    m_descriptionText->clear();
    updateButtons();
}

void FomodInstallerDialog::updateButtons()
{
    m_backBtn->setEnabled(m_currentStep > 0);
    m_nextBtn->setText(m_currentStep == m_stack->count() - 1 ? "Install" : "Next >");
}

void FomodInstallerDialog::onNext()
{
    if (m_currentStep == m_stack->count() - 1) {
        collectSelections();
        accept();
        return;
    }
    showStep(m_currentStep + 1);
}

void FomodInstallerDialog::onBack()
{
    if (m_currentStep == 0) return;
    showStep(m_currentStep - 1);
}

void FomodInstallerDialog::renderDescription(const QString& name, const QString& description)
{
    QString html;
    if (!name.isEmpty()) html += QString("<h3>%1</h3>").arg(name.toHtmlEscaped());
    if (!description.isEmpty())
        html += QString("<p>%1</p>").arg(description.toHtmlEscaped().replace("\n", "<br>"));
    m_descriptionText->setHtml(html);
}

void FomodInstallerDialog::collectSelections()
{
    m_selectedFiles.clear();
    m_selectedFiles.append(m_plan.requiredFiles);

    for (int s = 0; s < m_plan.steps.size() && s < m_stepWidgets.size(); ++s) {
        const auto& step = m_plan.steps[s];
        const auto& widgets = m_stepWidgets[s];
        for (int g = 0; g < step.groups.size() && g < widgets.pluginButtons.size(); ++g) {
            const auto& group = step.groups[g];
            const auto& buttons = widgets.pluginButtons[g];
            for (int p = 0; p < group.plugins.size() && p < buttons.size(); ++p) {
                QAbstractButton* btn = buttons[p];
                if (!btn) continue;
                if (btn->isChecked())
                    m_selectedFiles.append(group.plugins[p].files);
            }
        }
    }
}

} // namespace gorganizer
