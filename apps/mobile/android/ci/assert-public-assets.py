#!/usr/bin/env python3
"""拒绝把私钥、钥匙库或带 IP 的接口地址打进预发布附件。"""

import pathlib
import sys
import zipfile

NEEDLES = (
    b"-----BEGIN PRIVATE KEY-----",
    b"-----BEGIN RSA PRIVATE KEY-----",
    b"-----BEGIN OPENSSH PRIVATE KEY-----",
    b"-----BEGIN EC PRIVATE KEY-----",
    b"-----BEGIN PGP PRIVATE KEY BLOCK-----",
    b"YH_ANDROID_STORE_PASSWORD",
    b"YH_ANDROID_KEY_PASSWORD",
    b"http://10.0.2.2",
    b"https://10.0.2.2",
)
SECRET_NAMES = (".jks", ".keystore", ".p12", ".pem", ".key")


def fail(message: str) -> None:
    print(message, file=sys.stderr)
    sys.exit(1)


def scan_bytes(path: pathlib.Path, data: bytes) -> None:
    for needle in NEEDLES:
        if needle in data:
            fail(f"{path.name} 含有不能发布的内容：{needle.decode()}")


def scan_apk(path: pathlib.Path) -> None:
    with zipfile.ZipFile(path) as archive:
        for info in archive.infolist():
            name = info.filename.lower()
            if name.endswith(SECRET_NAMES) or "debug.keystore" in name or "signing.env" in name:
                fail(f"{path.name} 含有钥匙文件：{info.filename}")
            scan_bytes(path, archive.read(info))


def main() -> None:
    if len(sys.argv) < 2:
        fail("用法：assert-public-assets.py <file>...")
    for raw in sys.argv[1:]:
        path = pathlib.Path(raw)
        if not path.is_file():
            fail(f"找不到附件：{path}")
        if path.stat().st_size < 1000000:
            fail(f"附件过小：{path}")
        data = path.read_bytes()
        scan_bytes(path, data)
        if path.suffix == ".apk":
            scan_apk(path)
        print(f"ok {path.name} bytes={path.stat().st_size}")


if __name__ == "__main__":
    main()
