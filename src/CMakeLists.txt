add_subdirectory(util)
add_subdirectory(core)
add_subdirectory(grpc)
add_subdirectory(ui)

add_executable(gorganizer main.cpp)
target_link_libraries(gorganizer PRIVATE
    gorganizer_core
    gorganizer_ui
    gorganizer_util
    Qt6::Core
    Qt6::Widgets
)
