#include "PanelWidget.h"

#include <QTreeView>
#include <QStandardItemModel>
#include <QLabel>
#include <QVBoxLayout>

namespace gorganizer {

PanelWidget::PanelWidget(const QString& title, const QString& placeholderText,
                         QWidget* parent)
    : QWidget(parent)
    , m_defaultPlaceholder(placeholderText)
{
    auto* layout = new QVBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);

    auto* titleLabel = new QLabel(title);
    titleLabel->setStyleSheet("font-weight: bold;");
    layout->addWidget(titleLabel);

    m_model = new QStandardItemModel(this);

    m_treeView = new QTreeView;
    m_treeView->setModel(m_model);
    m_treeView->setRootIsDecorated(false);

    m_placeholder = new QLabel(m_defaultPlaceholder);
    m_placeholder->setAlignment(Qt::AlignCenter);
    m_placeholder->setStyleSheet("color: gray;");

    layout->addWidget(m_treeView);
    layout->addWidget(m_placeholder);

    m_treeView->hide();
    m_placeholder->show();
}

void PanelWidget::showContent()
{
    m_placeholder->hide();
    m_treeView->show();
}

void PanelWidget::showPlaceholder(const QString& text)
{
    m_treeView->hide();
    m_placeholder->setText(text.isEmpty() ? m_defaultPlaceholder : text);
    m_placeholder->show();
}

void PanelWidget::clearModel()
{
    m_model->removeRows(0, m_model->rowCount());
}

} // namespace gorganizer
