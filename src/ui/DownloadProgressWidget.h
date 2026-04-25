#pragma once

#include <QProgressBar>
#include "GrpcClient.h"

namespace gorganizer {

class DownloadProgressWidget : public QProgressBar {
    Q_OBJECT
public:
    explicit DownloadProgressWidget(GrpcClient* grpc, QWidget* parent = nullptr);

private slots:
    void onDownloadProgress(const GrpcDownloadProgress& progress);

private:
    GrpcClient* m_grpc;
};

} // namespace gorganizer
