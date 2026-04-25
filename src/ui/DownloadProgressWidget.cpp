#include "DownloadProgressWidget.h"

namespace gorganizer {

DownloadProgressWidget::DownloadProgressWidget(GrpcClient* grpc, QWidget* parent)
    : QProgressBar(parent)
    , m_grpc(grpc)
{
    setTextVisible(true);
    setVisible(false);

    connect(m_grpc, &GrpcClient::archiveEventReceived, this,
            [this](const GrpcArchiveEvent& evt) {
        if (evt.kind == GrpcArchiveEvent::KindDownloadProgress)
            onDownloadProgress(evt.progress);
    });
}

void DownloadProgressWidget::onDownloadProgress(const GrpcDownloadProgress& progress)
{
    // Unified DownloadStatus: 1=queued, 2=downloading, 3=downloaded,
    // 4=installing, 5=installed, 6=uninstalled, 7=cancelled, 8=failed. Hide
    // once the row reaches a terminal state.
    if (progress.status >= 3) {
        setVisible(false);
        return;
    }
    setVisible(true);

    if (progress.bytesTotal > 0) {
        setMaximum(100);
        setValue(static_cast<int>(progress.bytesDownloaded * 100 / progress.bytesTotal));
    } else {
        setMaximum(0); // Indeterminate.
    }

    QString statusText;
    switch (progress.status) {
    case 1: statusText = "Queued"; break;
    case 2: statusText = "Downloading"; break;
    default: statusText = "Working"; break;
    }
    setFormat(progress.modName + " - " + statusText + " %p%");
}

} // namespace gorganizer
