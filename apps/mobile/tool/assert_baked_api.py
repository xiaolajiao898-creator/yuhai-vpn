#!/usr/bin/env python3
"""确认发行产物里编进了 https://api.yhvpn.io，并且没有开发环境地址。"""

import pathlib
import sys
import zipfile

NEEDLE = b"https://api.yhvpn.io"
FORBIDDEN = b"http://10.0.2.2:8080"


def fail(message: str) -> None:
    print(message, file=sys.stderr)
    sys.exit(1)


def check_bytes(label: str, data: bytes) -> bool:
    if FORBIDDEN in data:
        fail(f"{label} 含有开发环境地址")
    return NEEDLE in data


def check_apk(path: pathlib.Path) -> None:
    found = False
    with zipfile.ZipFile(path) as archive:
        snapshots = [
            info
            for info in archive.infolist()
            if info.filename.endswith("libapp.so")
        ]
        if not snapshots:
            fail(f"{path} 里没有 libapp.so")
        for info in snapshots:
            if check_bytes(info.filename, archive.read(info)):
                found = True
                print(f"ok {info.filename}")
    if not found:
        fail(f"{path} 的 libapp.so 里没有 {NEEDLE.decode()}")


def check_file(path: pathlib.Path) -> None:
    if not check_bytes(path.name, path.read_bytes()):
        fail(f"{path} 里没有 {NEEDLE.decode()}")
    print(f"ok {path}")


def main() -> None:
    if len(sys.argv) < 2:
        fail("用法：assert_baked_api.py <apk-or-app.so>...")
    for raw in sys.argv[1:]:
        path = pathlib.Path(raw)
        if not path.is_file():
            fail(f"找不到 {path}")
        if path.suffix == ".apk":
            check_apk(path)
        else:
            check_file(path)


if __name__ == "__main__":
    main()
