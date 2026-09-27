#pragma once

#include <QString>

class QWidget;

namespace gorganizer {

// Opens a secure web link or warns when the URL is unsafe.
bool openWebLink(QWidget* parent, const QString& url);

}
