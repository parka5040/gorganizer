#include "GameInfo.h"
#include <algorithm>

namespace gorganizer {

const std::vector<GameInfo>& GameInfo::knownGames()
{
    static const std::vector<GameInfo> games = {
        {22320,  "The Elder Scrolls III: Morrowind",                "morrowind",  {}, {}, false},
        {22330,  "The Elder Scrolls IV: Oblivion",                  "oblivion",   {}, {}, false},
        {72850,  "The Elder Scrolls V: Skyrim",                     "skyrim",     {}, {}, false},
        {489830, "The Elder Scrolls V: Skyrim Special Edition",     "skyrimse",   {}, {}, false},
        {22370,  "Fallout 3",                                       "fallout3",   {}, {}, false},
        {22380,  "Fallout: New Vegas",                              "falloutnv",  {}, {}, false},
        {377160, "Fallout 4",                                       "fallout4",   {}, {}, false},
        {1716740,"Starfield",                                       "starfield",  {}, {}, false},
    };
    return games;
}

std::optional<GameInfo> GameInfo::findIn(const std::vector<GameInfo>& games, uint32_t appId)
{
    auto it = std::find_if(games.begin(), games.end(),
                           [appId](const GameInfo& g) { return g.appId == appId; });
    if (it != games.end())
        return *it;
    return std::nullopt;
}

} // namespace gorganizer
