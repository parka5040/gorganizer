#include "GameDetector.h"
#include "VdfParser.h"
#include "Paths.h"

#include <algorithm>
#include <set>

namespace {

std::filesystem::path relativePath(const QString& path)
{
    return std::filesystem::path(path.toStdString());
}

bool validInstallLayout(const std::filesystem::path& installDir,
                        const gorganizer::GameInfo& game,
                        std::filesystem::path* dataDirOut)
{
    std::error_code ec;
    const auto dataDir = installDir / relativePath(game.dataSubpath);
    bool dataDirMissing = false;
    if (game.dataDirOptional) {
        ec.clear();
        const auto dataStatus = std::filesystem::symlink_status(dataDir, ec);
        if (dataStatus.type() == std::filesystem::file_type::not_found) {
            dataDirMissing = true;
        } else if (ec || dataStatus.type() != std::filesystem::file_type::directory) {
            return false;
        }
    } else if (!std::filesystem::is_directory(dataDir, ec)) {
        return false;
    }

    if (!game.executablePaths.isEmpty()) {
        const bool foundMarker = std::any_of(
            game.executablePaths.begin(), game.executablePaths.end(),
            [&installDir](const QString& rel) {
                std::error_code markerEc;
                return std::filesystem::is_regular_file(
                    installDir / relativePath(rel), markerEc);
            });
        if (!foundMarker)
            return false;
    }

    if (!dataDirMissing) {
        for (const auto& rel : game.requiredDataFiles) {
            std::error_code requiredEc;
            if (!std::filesystem::is_regular_file(dataDir / relativePath(rel), requiredEc))
                return false;
        }
    }

    if (dataDirOut)
        *dataDirOut = dataDir;
    return true;
}

std::optional<std::filesystem::path> installRootForExecutable(
    const gorganizer::GameInfo& game, const std::filesystem::path& exePath)
{
    const QString selectedName = QString::fromStdString(exePath.filename().string());
    for (const auto& configured : game.executablePaths) {
        const auto rel = relativePath(configured);
        const QString configuredName = QString::fromStdString(rel.filename().string());
        if (configuredName.compare(selectedName, Qt::CaseInsensitive) != 0)
            continue;

        auto root = exePath;
        for (auto it = rel.begin(); it != rel.end(); ++it)
            root = root.parent_path();
        return root;
    }
    return std::nullopt;
}

}

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
    std::vector<std::filesystem::path> folders{steamRoot};
    auto parsed = VdfParser::parseFile(steamRoot / "steamapps" / "libraryfolders.vdf");
    if (!parsed)
        return folders;

    auto entries = parsed->value("libraryfolders").toMap();
    std::vector<std::pair<int, QString>> numbered;
    for (auto it = entries.begin(); it != entries.end(); ++it) {
        bool valid = false;
        int number = it.key().toInt(&valid);
        if (valid && number >= 0)
            numbered.emplace_back(number, it.key());
    }
    std::sort(numbered.begin(), numbered.end());
    for (const auto& item : numbered) {
        const auto value = entries.value(item.second);
        QString path = value.typeId() == QMetaType::QString
                           ? value.toString() : value.toMap().value("path").toString();
        if (path.isEmpty())
            continue;
        std::filesystem::path candidate(path.toStdString());
        std::error_code ec;
        if (!candidate.is_absolute() || !std::filesystem::is_directory(candidate / "steamapps", ec))
            continue;
        auto resolved = std::filesystem::canonical(candidate, ec);
        if (!ec && std::find(folders.begin(), folders.end(), resolved) == folders.end())
            folders.push_back(resolved);
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

    std::filesystem::path dataDir;
    if (!validInstallLayout(installDir, *it, &dataDir))
        return std::nullopt;

    GameInfo game = *it;
    const QString manifestName = appState.value("name").toString();
    if (!manifestName.isEmpty())
        game.name = manifestName;
    game.installDir = installDir;
    game.dataDir = dataDir;
    game.detected = true;
    return game;
}

std::vector<GameInfo> GameDetector::detectGames(const std::vector<std::filesystem::path>& libraryFolders)
{
    std::vector<GameInfo> detected;
    std::set<uint32_t> seenApps;
    for (const auto& folder : libraryFolders) {
        auto steamapps = folder / "steamapps";
        std::error_code ec;
        if (!std::filesystem::is_directory(steamapps, ec))
            continue;

        std::vector<std::filesystem::path> manifests;
        for (std::filesystem::directory_iterator it(steamapps, ec), end; !ec && it != end; it.increment(ec)) {
            auto filename = it->path().filename().string();
            if (filename.starts_with("appmanifest_") && filename.ends_with(".acf"))
                manifests.push_back(it->path());
        }
        std::sort(manifests.begin(), manifests.end());
        for (const auto& manifest : manifests) {
            auto game = parseAppManifest(manifest, folder);
            if (game && seenApps.insert(game->appId).second)
                detected.push_back(*game);
        }
    }

    std::sort(detected.begin(), detected.end(),
        [](const GameInfo& a, const GameInfo& b) { return a.appId < b.appId; });
    return detected;
}

std::vector<GameInfo> GameDetector::detectAll()
{
    std::vector<std::filesystem::path> folders;
    for (const auto& root : Paths::steamRoots()) {
        for (const auto& folder : findLibraryFolders(root)) {
            if (std::find(folders.begin(), folders.end(), folder) == folders.end())
                folders.push_back(folder);
        }
    }
    auto detected = detectGames(folders);

    bool hasFO3 = false, hasFNV = false;
    GameInfo fnv;
    for (const auto& g : detected) {
        if (g.shortName == "fallout3") hasFO3 = true;
        if (g.shortName == "falloutnv") { hasFNV = true; fnv = g; }
    }
    if (hasFO3 && hasFNV) {
        if (auto ttw = GameInfo::findByShortName("ttw")) {
            ttw->installDir = fnv.installDir;
            ttw->dataDir = fnv.dataDir;
            ttw->detected = true;
            detected.push_back(*ttw);
        }
    }
    return detected;
}

std::optional<GameInfo> GameDetector::fromExecutable(const std::filesystem::path& exePath)
{
    std::error_code ec;
    if (!std::filesystem::is_regular_file(exePath, ec))
        return std::nullopt;

    QString stem = QString::fromStdString(exePath.stem().string()).toLower();
    auto game = GameInfo::findByExeStem(stem);
    if (!game)
        return std::nullopt;

    auto installDir = installRootForExecutable(*game, exePath);
    if (!installDir)
        return std::nullopt;

    std::filesystem::path dataDir;
    if (!validInstallLayout(*installDir, *game, &dataDir))
        return std::nullopt;

    game->installDir = *installDir;
    game->dataDir = dataDir;
    game->detected = true;
    return game;
}

}
