#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

if [ "$#" -ne 2 ] || [ ! -d "$1" ]; then
    printf 'Usage: build-release.sh <src-dir> <out-dir>\n' >&2
    exit 2
fi
src="$(realpath "$1")"
mkdir -p "$2"
out="$(realpath "$2")"
work="$out/work"
mkdir -p "$work/source" "$work/downloads" "$out"
source "$src/packaging/release-inputs.lock"
export TZ=UTC LC_ALL=C GOTOOLCHAIN=local CGO_ENABLED=0
commit="${RELEASE_COMMIT:-$(git -C "$src" rev-parse HEAD)}"
commit_time="${RELEASE_COMMIT_TIME:-$(git -C "$src" show -s --format=%cI HEAD)}"
export SOURCE_DATE_EPOCH="$(date -u -d "$commit_time" +%s)"
commit_time="$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
version="$(tr -d '\n' < "$src/VERSION")"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && "$commit" =~ ^[0-9a-f]{40}$ ]] || { printf 'Invalid version or commit.\n' >&2; exit 1; }
short="${commit:0:12}"
bundle="$out/gorganizer-$version"
mkdir -p "$bundle/bin" "$bundle/lib" "$bundle/plugins" "$bundle/resources/icons" "$bundle/LICENSES"

if [ ! -x /opt/go/bin/go ]; then
    release_fetch "$GO_URL" "$GO_SHA256" "$work/downloads/go.tar.gz"
    mkdir -p /opt/go
    tar -xz -C /opt -f "$work/downloads/go.tar.gz"
fi
export PATH="/opt/go/bin:$work/tools/bin:/opt/grpc/bin:$PATH"

if [ ! -x /opt/grpc/bin/grpc_cpp_plugin ]; then
    git clone --branch "$GRPC_TAG" --depth 1 --recurse-submodules --shallow-submodules "$GRPC_REPO" /opt/grpc-source
    [ "$(git -C /opt/grpc-source rev-parse HEAD)" = "$GRPC_COMMIT" ] || { printf 'gRPC commit does not match lock.\n' >&2; exit 1; }
    flags='-ffile-prefix-map=/opt/grpc-source=. -ffile-prefix-map=/opt/grpc=.'
    cmake -S /opt/grpc-source/third_party/abseil-cpp -B /opt/grpc-absl-build -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/grpc \
        -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DBUILD_SHARED_LIBS=OFF \
        -DABSL_ENABLE_INSTALL=ON "-DCMAKE_CXX_FLAGS=$flags"
    cmake --build /opt/grpc-absl-build --parallel "$(nproc)"
    cmake --install /opt/grpc-absl-build
    cmake -S /opt/grpc-source/third_party/protobuf -B /opt/grpc-protobuf-build -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/grpc \
        -DCMAKE_PREFIX_PATH=/opt/grpc -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
        -Dprotobuf_BUILD_TESTS=OFF -Dprotobuf_ABSL_PROVIDER=package \
        -Dprotobuf_BUILD_SHARED_LIBS=OFF "-DCMAKE_CXX_FLAGS=$flags" "-DCMAKE_C_FLAGS=$flags"
    cmake --build /opt/grpc-protobuf-build --parallel "$(nproc)"
    cmake --install /opt/grpc-protobuf-build
    cmake -S /opt/grpc-source -B /opt/grpc-build -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/grpc \
        -DCMAKE_PREFIX_PATH=/opt/grpc -DgRPC_INSTALL=ON -DgRPC_BUILD_TESTS=OFF \
        -DBUILD_SHARED_LIBS=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
        -DgRPC_PROTOBUF_PROVIDER=package -DgRPC_ABSL_PROVIDER=package \
        -DgRPC_CARES_PROVIDER=module -DgRPC_RE2_PROVIDER=module \
        -DgRPC_ZLIB_PROVIDER=module -DgRPC_SSL_PROVIDER=module \
        "-DCMAKE_CXX_FLAGS=$flags" "-DCMAKE_C_FLAGS=$flags"
    cmake --build /opt/grpc-build --parallel "$(nproc)"
    cmake --install /opt/grpc-build
fi
[ "$(git -C /opt/grpc-source rev-parse HEAD)" = "$GRPC_COMMIT" ] || exit 1
if git -C /opt/grpc-source submodule status --recursive | grep -qE '^[-+U]'; then
    printf 'gRPC has an uninitialized or mismatched submodule.\n' >&2
    exit 1
fi

qt_root="/opt/qt/$QT_VERSION/gcc_64"
if [ ! -d "$qt_root/lib/cmake/Qt6" ]; then
    mkdir -p /opt/qt
    for n in 1 2 3 4; do
        archive_var="QT_ARCHIVE_$n" hash_var="QT_ARCHIVE_${n}_SHA256"
        release_fetch "$QT_BASE_URL/${!archive_var}" "${!hash_var}" "$work/downloads/${!archive_var}"
        destination="$qt_root"
        case "${!archive_var}" in *icu-linux-*) destination="$qt_root/lib" ;; esac
        mkdir -p "$destination"
        7z x -y "-o$destination" "$work/downloads/${!archive_var}" >/dev/null
    done
fi
[ -d "$qt_root/lib/cmake/Qt6" ] || { printf 'Qt archive layout does not contain Qt6 CMake packages.\n' >&2; exit 1; }

cp -a "$src/api" "$src/cmd" "$src/internal" "$src/src" "$src/resources" "$work/source/"
cp "$src/go.mod" "$src/go.sum" "$src/Makefile" "$src/CMakeLists.txt" "$src/VERSION" "$work/source/"
rm -f "$work/source/api/proto/"*.pb.go
cd "$work/source"
export GOBIN="$work/tools/bin" GOCACHE="$work/gocache" GOMODCACHE="$work/gomodcache"
mkdir -p "$GOBIN" "$GOCACHE" "$GOMODCACHE"
proto_version="$(go list -m -f '{{.Version}}' google.golang.org/protobuf)"
grpc_gen_version="$(go list -m -f '{{.Version}}' google.golang.org/grpc/cmd/protoc-gen-go-grpc)"
go install "google.golang.org/protobuf/cmd/protoc-gen-go@$proto_version"
go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@$grpc_gen_version"
protoc --version
protoc --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative api/proto/gorganizer.proto
ldflags="-s -w -buildid= -X main.version=$version+$short -X main.commit=$commit -X main.buildDate=$commit_time"
for name in gorganizerd gorganizerctl; do
    go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$bundle/bin/$name" "./cmd/$name"
done
cmake -S . -B "$work/gui" -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DGORGANIZER_RELEASE=ON -DGORGANIZER_VERSION="$version" -DCMAKE_PREFIX_PATH="$qt_root;/opt/grpc" \
    -DProtobuf_DIR=/opt/grpc/lib/cmake/protobuf \
    -DgRPC_DIR=/opt/grpc/lib/cmake/grpc -DPROTOC=/opt/grpc/bin/protoc \
    -DgRPC_CPP_PLUGIN=/opt/grpc/bin/grpc_cpp_plugin \
    "-DCMAKE_CXX_FLAGS=-ffile-prefix-map=$work=. -ffile-prefix-map=/opt/grpc-source=."
cmake --build "$work/gui" --parallel "$(nproc)"
cp "$work/gui/src/gorganizer" "$bundle/bin/gorganizer-gui"
strip --strip-unneeded "$bundle/bin/gorganizer-gui"
cat > "$bundle/bin/qt.conf" <<'EOF'
[Paths]
Prefix=..
Libraries=lib
Plugins=plugins
EOF
cp "$src/resources/icons/tmp_logo.png" "$bundle/resources/icons/tmp_logo.png"
cp "$src/gorganizer.sh" "$src/cleaner.sh" "$bundle/"
chmod 755 "$bundle/gorganizer.sh" "$bundle/cleaner.sh"

plugin_root="$qt_root/plugins"
shopt -s nullglob
plugins=(
    "$plugin_root"/platforms/libqxcb.so
    "$plugin_root"/platforms/libqwayland-*.so
    "$plugin_root"/platforms/libqoffscreen.so
    "$plugin_root"/wayland-*/*.so
    "$plugin_root"/xcbglintegrations/*.so
    "$plugin_root"/iconengines/libqsvgicon.so
    "$plugin_root"/imageformats/libqsvg.so
    "$plugin_root"/platforminputcontexts/*.so
)
for required in platforms/libqxcb.so platforms/libqoffscreen.so iconengines/libqsvgicon.so imageformats/libqsvg.so; do
    [ -f "$plugin_root/$required" ] || { printf 'Missing Qt plugin: %s\n' "$required" >&2; exit 1; }
done
for plugin in "${plugins[@]}"; do
    dest="$bundle/plugins/${plugin#"$plugin_root"/}"
    mkdir -p "$(dirname "$dest")"
    cp -L "$plugin" "$dest"
    patchelf --set-rpath '$ORIGIN/../../lib' "$dest"
done
patchelf --set-rpath '$ORIGIN/../lib' "$bundle/bin/gorganizer-gui"

# Only dependencies present in the pinned Qt/ICU archives are copied.
# System dependencies remain dependencies on the host and are audited below.
queue=("$bundle/bin/gorganizer-gui")
for plugin in "${plugins[@]}"; do queue+=("$bundle/plugins/${plugin#"$plugin_root"/}"); done
while [ "${#queue[@]}" -ne 0 ]; do
    current="${queue[0]}"
    queue=("${queue[@]:1}")
    while IFS= read -r name; do
        [ -n "$name" ] || continue
        [ ! -e "$bundle/lib/$name" ] || continue
        candidate="$qt_root/lib/$name"
        if [ ! -f "$candidate" ]; then
            candidate="$(find /opt/qt \( -type f -o -type l \) -name "$name" -print -quit)"
        fi
        if [ -f "$candidate" ]; then
            [[ "$name" == libQt6*.so* || "$name" == libicu*.so* ]] || {
                printf 'Refusing to bundle non-Qt/ICU library: %s\n' "$name" >&2; exit 1;
            }
            cp -L "$candidate" "$bundle/lib/$name"
            patchelf --set-rpath '$ORIGIN' "$bundle/lib/$name"
            queue+=("$bundle/lib/$name")
        fi
    done < <(release_needed "$current")
done

cp "$src/LICENSE" "$bundle/LICENSES/Gorganizer-GPL-3.0.txt"
cp /usr/share/common-licenses/LGPL-3 "$bundle/LICENSES/Qt-LGPL-3.0.txt"
for entry in \
    'gRPC:/opt/grpc-source/LICENSE' \
    'protobuf:/opt/grpc-source/third_party/protobuf/LICENSE' \
    'abseil:/opt/grpc-source/third_party/abseil-cpp/LICENSE' \
    're2:/opt/grpc-source/third_party/re2/LICENSE' \
    'c-ares:/opt/grpc-source/third_party/cares/cares/LICENSE.md' \
    'zlib:/opt/grpc-source/third_party/zlib/LICENSE' \
    'boringssl:/opt/grpc-source/third_party/boringssl-with-bazel/LICENSE' \
    'utf8_range:/opt/grpc-source/third_party/utf8_range/LICENSE' \
    'address_sorting:/opt/grpc-source/third_party/address_sorting/LICENSE' \
    'xxhash:/opt/grpc-source/third_party/xxhash/LICENSE'; do
    name="${entry%%:*}" path="${entry#*:}"
    if [ ! -f "$path" ]; then
        case "$name" in
            zlib) path=/opt/grpc-source/third_party/zlib/zlib.h ;;
            boringssl) path=/opt/grpc-source/third_party/boringssl-with-bazel/LICENSE ;;
            c-ares) path=/opt/grpc-source/third_party/cares/cares/LICENSE ;;
        esac
    fi
    [ -f "$path" ] || { printf 'Missing license: %s\n' "$path" >&2; exit 1; }
    cp "$path" "$bundle/LICENSES/$name.txt"
done
cp /usr/share/doc/libicu70/copyright "$bundle/LICENSES/ICU.txt"
printf 'Bundled Qt %s components were downloaded from:\n%s/%s\n%s/%s\n%s/%s\nCorresponding Qt sources are available at https://download.qt.io/archive/qt/%s/%s/single/.\n' \
    "$QT_VERSION" "$QT_BASE_URL" "$QT_ARCHIVE_1" "$QT_BASE_URL" "$QT_ARCHIVE_2" \
    "$QT_BASE_URL" "$QT_ARCHIVE_3" "${QT_VERSION%.*}" "$QT_VERSION" > "$bundle/LICENSES/Qt-sources.txt"

inputs_sha256="$(sha256sum "$src/packaging/release-inputs.lock" | cut -d ' ' -f 1)"
export RELEASE_VERSION="$version" RELEASE_COMMIT_ID="$commit" RELEASE_COMMIT_TIME="$commit_time" \
    RELEASE_INPUTS_SHA="$inputs_sha256" RELEASE_BUILDER_IMAGE="$BUILDER_IMAGE" \
    RELEASE_GRPC_SOURCE=/opt/grpc-source RELEASE_LOCK="$src/packaging/release-inputs.lock"
python3 - "$bundle" <<'PY'
import json
import os
import subprocess
import sys
from pathlib import Path

bundle = Path(sys.argv[1])
def save(name, obj):
    (bundle / name).write_text(json.dumps(obj, sort_keys=True, separators=(',', ':')) + '\n')

save('release.json', dict(version=os.environ['RELEASE_VERSION'], commit=os.environ['RELEASE_COMMIT_ID'],
    commit_time=os.environ['RELEASE_COMMIT_TIME'], inputs_sha256=os.environ['RELEASE_INPUTS_SHA'],
    builder_image=os.environ['RELEASE_BUILDER_IMAGE']))
lock = dict(line.split('=', 1) for line in Path(os.environ['RELEASE_LOCK']).read_text().splitlines()
            if line and not line.startswith('#'))
inputs = [dict(name='Go', version=lock['GO_VERSION'], source=lock['GO_URL'], hash=lock['GO_SHA256']),
          dict(name='gRPC', version=lock['GRPC_TAG'], source=lock['GRPC_REPO'], hash=lock['GRPC_COMMIT']),
          dict(name='builder', version='Ubuntu 22.04', source=lock['BUILDER_IMAGE'],
               hash=lock['BUILDER_IMAGE'].split('@sha256:')[1])]
for n in range(1, 5):
    inputs.append(dict(name=f'Qt archive {n}', version=lock['QT_VERSION'],
        source=lock['QT_BASE_URL'] + '/' + lock[f'QT_ARCHIVE_{n}'], hash=lock[f'QT_ARCHIVE_{n}_SHA256']))
for key in ('ACTIONS_CHECKOUT', 'ACTIONS_UPLOAD_ARTIFACT', 'ACTIONS_DOWNLOAD_ARTIFACT'):
    source, sha = lock[key].split('@')
    inputs.append(dict(name=key, version=sha, source='https://github.com/' + source, hash=sha))
submodules = subprocess.check_output(['git', '-C', os.environ['RELEASE_GRPC_SOURCE'],
    'submodule', 'status', '--recursive'], text=True).splitlines()
packages = subprocess.check_output(['dpkg-query', '-W', '-f=${binary:Package}\t${Version}\n'], text=True).splitlines()
save('SBOM.json', dict(inputs=inputs, grpc_submodules=[dict(commit=s[1:41], path=s[42:].split(' ')[0])
    for s in submodules], apt_packages=[dict(name=p.split('\t')[0], version=p.split('\t')[1]) for p in packages]))
PY
(cd "$bundle" && while IFS= read -r -d '' path; do sha256sum "${path#./}"; done \
    < <(find . -type f ! -name MANIFEST.sha256 -print0 | sort -z) > MANIFEST.sha256)
RELEASE_BUILD_DIR="$work/" "$src/packaging/audit-release.sh" "$bundle"
tarball="$out/gorganizer-$version-linux-x86_64.tar.gz"
release_tar "$bundle" "$tarball" "$SOURCE_DATE_EPOCH"
(cd "$out" && sha256sum "$(basename "$tarball")" > SHA256SUMS)
if [ -n "${RELEASE_OWNER:-}" ]; then chown -R "$RELEASE_OWNER" "$out"; fi
printf 'Built %s\n' "$tarball"
