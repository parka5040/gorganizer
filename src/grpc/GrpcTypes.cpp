#include "GrpcTypes.h"

#include "GameInfo.h"

namespace gorganizer {

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
    return game;
}

}
