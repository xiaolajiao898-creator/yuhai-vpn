#!/usr/bin/env python3
"""打印真机发行构建的 --dart-define。缺少必填项时退出码为 1。

必填：
  YH_API_BASE  必须正好是 https://api.yhvpn.io

有默认值、会写进包里：
  YH_FUND_KEY_ID_1  默认 fund-prod-1
  YH_FUND_KEY_ID_2  默认 fund-prod-2

可选。空着时付款地址验签会失败，注册和连接不依赖它们：
  YH_FUND_PUBKEY_1
  YH_FUND_PUBKEY_2

可选。非法时构建失败，不传则关于页不显示链接：
  YH_OFFICIAL_URL
  YH_PRIVACY_URL
  YH_TERMS_URL
"""

import base64
import os
import sys
from urllib.parse import urlparse

API = "https://api.yhvpn.io"


def fail(message: str) -> None:
    print(message, file=sys.stderr)
    sys.exit(1)


def required_api() -> str:
    value = os.environ.get("YH_API_BASE", "")
    if value == "":
        fail("缺少必填配置 YH_API_BASE")
    if value != API:
        fail(f"YH_API_BASE 必须是 {API}，当前是 {value!r}")
    return value


def key_id(name: str, default: str) -> str:
    value = os.environ.get(name, default)
    if value == "":
        fail(f"{name} 不能是空字符串")
    if value != value.strip() or any(ord(ch) < 32 for ch in value) or len(value) > 64:
        fail(f"{name} 无效")
    return value


def optional_pubkey(name: str) -> str | None:
    if name not in os.environ:
        return None
    value = os.environ[name]
    if value == "":
        fail(f"{name} 已设置但不能为空")
    try:
        raw = base64.b64decode(value, validate=True)
    except Exception:
        fail(f"{name} 不是标准 Base64")
        return None
    if len(raw) != 32:
        fail(f"{name} 必须是 32 字节的 Ed25519 公钥")
    return value


def optional_https(name: str) -> str | None:
    if name not in os.environ:
        return None
    value = os.environ[name]
    if value == "":
        fail(f"{name} 已设置但不能为空")
    parsed = urlparse(value)
    host = parsed.hostname or ""
    if (
        parsed.scheme != "https"
        or host == ""
        or parsed.username is not None
        or parsed.query
        or parsed.fragment
        or (parsed.path not in ("", "/"))
    ):
        fail(f"{name} 必须是不带账号、查询或路径的 HTTPS 地址")
    return value


def emit(name: str, value: str) -> None:
    if any(ch.isspace() for ch in value):
        fail(f"{name} 不能包含空白")
    print(f"--dart-define={name}={value}")


def main() -> None:
    emit("YH_API_BASE", required_api())
    emit("YH_FUND_KEY_ID_1", key_id("YH_FUND_KEY_ID_1", "fund-prod-1"))
    emit("YH_FUND_KEY_ID_2", key_id("YH_FUND_KEY_ID_2", "fund-prod-2"))
    for name in ("YH_FUND_PUBKEY_1", "YH_FUND_PUBKEY_2"):
        value = optional_pubkey(name)
        if value is not None:
            emit(name, value)
    for name in ("YH_OFFICIAL_URL", "YH_PRIVACY_URL", "YH_TERMS_URL"):
        value = optional_https(name)
        if value is not None:
            emit(name, value)


if __name__ == "__main__":
    main()
