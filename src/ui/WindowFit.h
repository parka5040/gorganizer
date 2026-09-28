#pragma once

#include <QSize>

class QWidget;

namespace gorganizer {

void fitToScreen(QWidget* window, QSize preferred, QSize minimum = {});
void clampToScreen(QWidget* window);

}
