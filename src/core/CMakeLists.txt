add_library(gorganizer_core STATIC
    VdfParser.cpp
    GameInfo.cpp
    GameDetector.cpp
    AppConfig.cpp
    DirectoryManager.cpp
    PluginScanner.cpp
)
target_include_directories(gorganizer_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(gorganizer_core PUBLIC gorganizer_util Qt6::Core)
