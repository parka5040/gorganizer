#include "GrpcTypes.h"

#include "GameInfo.h"
#include "GrpcInstallPreview.h"

namespace gorganizer {

static_assert(static_cast<int>(GrpcInstallAsNewMod) == gorganizer::v1::INSTALL_MODE_NEW_MOD);
static_assert(static_cast<int>(GrpcInstallMergeIntoMod) == gorganizer::v1::INSTALL_MODE_MERGE_INTO);
static_assert(static_cast<int>(GrpcInstallReplaceMod) == gorganizer::v1::INSTALL_MODE_REPLACE);

namespace {

GrpcFomodPlan fomodPlanFromProto(const gorganizer::v1::FomodPlan& p)
{
    GrpcFomodPlan out;
    out.moduleName = QString::fromStdString(p.module_name());
    out.modulePath = QString::fromStdString(p.module_path());
    for (const auto& f : p.required_files()) {
        out.requiredFiles.push_back(GrpcFomodFile{
            QString::fromStdString(f.source()),
            QString::fromStdString(f.destination()),
            f.is_folder(),
            f.priority(),
        });
    }
    for (const auto& step : p.steps()) {
        GrpcFomodStep s;
        s.name = QString::fromStdString(step.name());
        for (const auto& g : step.groups()) {
            GrpcFomodGroup gg;
            gg.name = QString::fromStdString(g.name());
            gg.type = static_cast<int>(g.type());
            for (const auto& plugin : g.plugins()) {
                GrpcFomodPlugin pp;
                pp.name = QString::fromStdString(plugin.name());
                pp.description = QString::fromStdString(plugin.description());
                pp.imagePath = QString::fromStdString(plugin.image_path());
                pp.defaultState = static_cast<int>(plugin.default_state());
                for (const auto& f : plugin.files()) {
                    pp.files.push_back(GrpcFomodFile{
                        QString::fromStdString(f.source()),
                        QString::fromStdString(f.destination()),
                        f.is_folder(),
                        f.priority(),
                    });
                }
                gg.plugins.push_back(std::move(pp));
            }
            s.groups.push_back(std::move(gg));
        }
        out.steps.push_back(std::move(s));
    }
    out.legacyInfoOnly = p.legacy_info_only();
    out.description = QString::fromStdString(p.description());
    out.screenshotPath = QString::fromStdString(p.screenshot_path());
    out.version = QString::fromStdString(p.version());
    out.author = QString::fromStdString(p.author());
    out.moduleConfigXml = QByteArray::fromStdString(p.module_config_xml());
    out.screenshotData = QByteArray::fromStdString(p.screenshot_data());
    return out;
}

}

gorganizer::v1::PreviewInstallRequest previewInstallRequest(const QString& gameId,
                                                            const QString& archiveRelPath,
                                                            const QString& externalArchivePath)
{
    gorganizer::v1::PreviewInstallRequest req;
    req.set_game_id(gameId.toStdString());
    if (!archiveRelPath.isEmpty()) req.set_archive_rel_path(archiveRelPath.toStdString());
    if (!externalArchivePath.isEmpty()) req.set_external_archive_path(externalArchivePath.toStdString());
    return req;
}

GrpcPreviewInstallResult previewInstallResultFromProto(const gorganizer::v1::PreviewInstallResponse& response)
{
    GrpcPreviewInstallResult out;
    out.previewId = QString::fromStdString(response.preview_id());
    out.hasFomod = response.has_fomod();
    if (response.has_plan()) out.plan = fomodPlanFromProto(response.plan());
    for (const auto& f : response.flat_file_list()) out.flatFileList.append(QString::fromStdString(f));
    for (const auto& root : response.selectable_roots())
        out.selectableRoots.append(QString::fromStdString(root));
    out.detectedRoot = QString::fromStdString(response.detected_root());
    out.rootAmbiguous = response.root_ambiguous();
    return out;
}

GameInfo toGameInfo(const GrpcGame& game)
{
    GameInfo info;
    info.appId = game.steamAppId;
    info.name = game.name;
    info.shortName = game.gameId;
    info.installDir = game.installPath.toStdString();
    info.dataDir = game.dataPath.toStdString();
    info.detected = true;
    info.synthetic = game.synthetic;
    info.linkedFromShortName = game.linkedFromGameId;
    info.vfsActive = game.vfsActive;
    info.capabilities = game.capabilities;
    info.capabilitiesKnown = game.capabilitiesKnown;
    return info;
}

GrpcGame toGrpcGame(const GameInfo& info)
{
    GrpcGame game;
    game.gameId = info.shortName;
    game.name = info.name;
    game.steamAppId = info.appId;
    game.installPath = QString::fromStdString(info.installDir.string());
    game.dataPath = QString::fromStdString(info.dataDir.string());
    game.synthetic = info.synthetic;
    game.linkedFromGameId = info.linkedFromShortName;
    game.vfsActive = info.vfsActive;
    game.capabilities = info.capabilities;
    game.capabilitiesKnown = info.capabilitiesKnown;
    return game;
}

bool grpcOutcomeUnknown(int code)
{
    return code == GrpcStatusCancelled || code == GrpcStatusUnknown || code == GrpcStatusDeadlineExceeded
        || code == GrpcStatusUnavailable;
}

}
