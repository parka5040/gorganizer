#include "GameDetector.h"
#include "VdfParser.h"
#include "Paths.h"

namespace gorganizer {

std::optional<std::filesystem::path> GameDetector::findSteamRoot()
{
    auto root = Paths::steamRoot();
    if (root.empty())
        return std::nullopt;
    return root;
}

std::vector<std::filesystem::path> GameDetector::findLibraryFolders(const std::filesystem::path& steamRoot)
{
    std::vector<std::filesystem::path> folders;
    auto vdfPath = steamRoot / "steamapps" / "libraryfolders.vdf";
    auto parsed = VdfParser::parseFile(vdfPath);
    if (!parsed)
        return folders;

    auto root = parsed->value("libraryfolders").toMap();
    for (auto it = root.begin(); it != root.end(); ++it) {
        auto entry = it.value().toMap();
        QString path = entry.value("path").toString();
        if (!path.isEmpty()) {
            std::filesystem::path p(path.toStdString());
            if (std::filesystem::exists(p / "steamapps"))
                folders.push_back(p);
        }
    }
    return folders;
}

std::optional<GameInfo> GameDetector::parseAppManifest(
    const std::filesystem::path& acfPath,
    const std::filesystem::path& libraryFolder)
{
    auto parsed = VdfParser::parseFile(acfPath);
    if (!parsed)
        return std::nullopt;

    auto appState = parsed->value("AppState").toMap();
    if (appState.isEmpty())
        return std::nullopt;

    uint32_t appId = appState.value("appid").toString().toUInt();
    if (appId == 0)
        return std::nullopt;

    // Check against known Bethesda games
    const auto& known = GameInfo::knownGames();
    auto it = std::find_if(known.begin(), known.end(),
        [appId](const GameInfo& g) { return g.appId == appId; });
    if (it == known.end())
        return std::nullopt;

    QString installDirName = appState.value("installdir").toString();
    if (installDirName.isEmpty())
        return std::nullopt;

    auto installDir = libraryFolder / "steamapps" / "common"
                      / installDirName.toStdString();
    if (!std::filesystem::exists(installDir))
        return std::nullopt;

    auto dataDir = installDir / "Data";
    if (!std::filesystem::exists(dataDir))
        return std::nullopt;

    GameInfo game = *it;
    game.name = appState.value("name").toString();
    game.installDir = installDir;
    game.dataDir = dataDir;
    game.detected = true;
    return game;
}

std::vector<GameInfo> GameDetector::detectGames(const std::vector<std::filesystem::path>& libraryFolders)
{
    std::vector<GameInfo> detected;
    for (const auto& folder : libraryFolders) {
        auto steamapps = folder / "steamapps";
        if (!std::filesystem::exists(steamapps))
            continue;

        for (const auto& entry : std::filesystem::directory_iterator(steamapps)) {
            auto filename = entry.path().filename().string();
            if (filename.starts_with("appmanifest_") && filename.ends_with(".acf")) {
                auto game = parseAppManifest(entry.path(), folder);
                if (game)
                    detected.push_back(*game);
            }
        }
    }

    // Sort by app ID for consistent ordering
    std::sort(detected.begin(), detected.end(),
        [](const GameInfo& a, const GameInfo& b) { return a.appId < b.appId; });
    return detected;
}

std::vector<GameInfo> GameDetector::detectAll()
{
    auto root = findSteamRoot();
    if (!root)
        return {};
    auto folders = findLibraryFolders(*root);
    return detectGames(folders);
}

std::optional<GameInfo> GameDetector::fromExecutable(const std::filesystem::path& exePath)
{
    if (!std::filesystem::exists(exePath))
        return std::nullopt;

    // Map of main executable filename → shortName. Case-insensitive on stem.
    static const std::vector<std::pair<QString, QString>> exeMap = {
        {"morrowind", "morrowind"},
        {"oblivion",  "oblivion"},
        {"tesv",      "skyrim"},
        {"skyrimse",  "skyrimse"},
        {"fallout3",  "fallout3"},
        {"falloutnv", "falloutnv"},
        {"fallout4",  "fallout4"},
        {"starfield", "starfield"},
    };

    QString stem = QString::fromStdString(exePath.stem().string()).toLower();
    QString shortName;
    for (const auto& [needle, id] : exeMap) {
        if (stem == needle) {
            shortName = id;
            break;
        }
    }
    if (shortName.isEmpty())
        return std::nullopt;

    const auto& known = GameInfo::knownGames();
    auto it = std::find_if(known.begin(), known.end(),
        [&](const GameInfo& g) { return g.shortName == shortName; });
    if (it == known.end())
        return std::nullopt;

    auto installDir = exePath.parent_path();
    auto dataDir = installDir / "Data";

    GameInfo game = *it;
    game.installDir = installDir;
    game.dataDir = dataDir;
    game.detected = true;
    return game;
}

} // namespace gorganizer
