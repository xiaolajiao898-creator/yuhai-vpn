import 'dart:ffi';
import 'dart:io';

import 'package:ffi/ffi.dart';

import 'windows_system_tools.dart';

/// 一条允许通过的进程，以及拦住其他所有 IPv4/IPv6 出站连接的计划。
class TrafficGuardPlan {
  const TrafficGuardPlan(
      {required this.permitPaths, required this.blockAllOthers});
  final List<String> permitPaths;
  final bool blockAllOthers;

  static const permitWeight = 15;
  static const blockWeight = 1;
}

/// Windows 断网保护。
///
/// 用动态 WFP 会话在 ALE_AUTH_CONNECT（v4/v6）拦住出站连接，只放行：名单里的程序
/// （本程序、xray.exe、wireguard.exe）和本机回环。系统 DNS 客户端（svchost）也在拦截范围内，
/// 所以拦着的时候别的程序查不了域名，也不会经本地网络泄露查询。
/// 会话句柄随进程结束而关闭，过滤规则一起消失，所以崩溃不会把电脑的网络永久掐死。
/// 规则没有装上时必须返回 false，界面不能说「已保护」。
///
/// 结构体布局按 x64（fwpmtypes.h）：FWPM_SESSION0 72 字节（flags 在 32），
/// FWPM_SUBLAYER0 72 字节（weight 在 64），FWPM_FILTER0 200 字节，
/// FWPM_FILTER_CONDITION0 40 字节（conditionValue.type 在 24，值/指针在 32）。
class WindowsTrafficGuard {
  WindowsTrafficGuard({DynamicLibrary? library}) : _library = library;

  final DynamicLibrary? _library;
  int? _engine;
  DynamicLibrary? _opened;

  bool get held => _engine != null;

  /// 已经拦着时直接返回 true，不先拆再装（拆和装之间会有一个没有规则的空档）。
  Future<bool> hold(List<String> permitPaths) async {
    if (!Platform.isWindows) return false;
    if (_engine != null) return true;
    final paths = permitPaths.where((path) => path.isNotEmpty).toSet().toList();
    if (paths.isEmpty) return false;
    try {
      // 以管理员身份运行：只从 System32 加载，不让程序目录或当前目录里的同名 DLL 被加载。
      final fwpu = _opened = _library ??
          DynamicLibrary.open('${windowsSystemRoot()}\\System32\\fwpuclnt.dll');
      final engine = calloc<IntPtr>();
      final session = calloc<Uint8>(72);
      try {
        // FWPM_SESSION_FLAG_DYNAMIC：进程退出、句柄关闭后规则消失。
        session[32] = 1;
        final open = fwpu.lookupFunction<
            Int32 Function(Pointer<Utf16>, Uint32, Pointer<Void>,
                Pointer<Uint8>, Pointer<IntPtr>),
            int Function(Pointer<Utf16>, int, Pointer<Void>, Pointer<Uint8>,
                Pointer<IntPtr>)>('FwpmEngineOpen0');
        final opened = open(nullptr, 10, nullptr, session, engine);
        if (opened != 0 || engine.value == 0) return false;
        _engine = engine.value;
        if (!_install(fwpu, engine.value, paths)) {
          release();
          return false;
        }
        return true;
      } finally {
        calloc.free(session);
        calloc.free(engine);
      }
    } catch (_) {
      release();
      return false;
    }
  }

  void release() {
    final engine = _engine;
    _engine = null;
    if (engine == null) return;
    try {
      final fwpu = _opened;
      if (fwpu != null) {
        final close =
            fwpu.lookupFunction<Int32 Function(IntPtr), int Function(int)>(
                'FwpmEngineClose0');
        close(engine);
      }
    } catch (_) {}
  }

  bool _install(DynamicLibrary fwpu, int engine, List<String> paths) {
    final begin = fwpu.lookupFunction<Int32 Function(IntPtr, Uint32),
        int Function(int, int)>('FwpmTransactionBegin0');
    final commit =
        fwpu.lookupFunction<Int32 Function(IntPtr), int Function(int)>(
            'FwpmTransactionCommit0');
    final abort =
        fwpu.lookupFunction<Int32 Function(IntPtr), int Function(int)>(
            'FwpmTransactionAbort0');
    if (begin(engine, 0) != 0) return false;
    var ok = _addSublayer(fwpu, engine);
    if (ok) {
      ok = _addBlock(fwpu, engine, _connectV4) &&
          _addBlock(fwpu, engine, _connectV6);
    }
    if (ok) {
      // 本机回环不出网卡，拦了只会弄坏别的程序的本地通信。
      final loopback = [
        const _Condition(_conditionFlags, _uint32, _flagIsLoopback,
            match: _matchFlagsAllSet)
      ];
      ok = _addPermit(fwpu, engine, _connectV4, loopback) &&
          _addPermit(fwpu, engine, _connectV6, loopback);
    }
    if (ok) {
      // 只放行发往 Xray TUN 自己那个 DNS 地址（172.19.0.2:53）的查询。这个地址只在 xray0
      // 网卡上，查询进的是隧道；否则新隧道起来后，系统 DNS 客户端（svchost）也被拦，
      // 连接探测解析不了域名，规则就永远撤不掉。
      ok = _addPermit(fwpu, engine, _connectV4, const [
        _Condition(_remoteAddress, _uint32, _tunDnsV4),
        _Condition(_remotePort, _uint16, 53),
      ]);
    }
    if (ok) {
      // DHCP 续租（UDP 68→67 / 546→547），拦得久了网卡会丢地址。
      ok = _addPermit(fwpu, engine, _connectV4, const [
            _Condition(_ipProtocol, _uint8, 17),
            _Condition(_localPort, _uint16, 68),
            _Condition(_remotePort, _uint16, 67),
          ]) &&
          _addPermit(fwpu, engine, _connectV6, const [
            _Condition(_ipProtocol, _uint8, 17),
            _Condition(_localPort, _uint16, 546),
            _Condition(_remotePort, _uint16, 547),
          ]);
    }
    for (final path in paths) {
      if (!ok) break;
      ok = _addAppPermit(fwpu, engine, path, _connectV4) &&
          _addAppPermit(fwpu, engine, path, _connectV6);
    }
    if (!ok) {
      abort(engine);
      return false;
    }
    return commit(engine) == 0;
  }

  bool _addSublayer(DynamicLibrary fwpu, int engine) {
    final add = fwpu.lookupFunction<
        Int32 Function(IntPtr, Pointer<Uint8>, Pointer<Void>),
        int Function(int, Pointer<Uint8>, Pointer<Void>)>('FwpmSubLayerAdd0');
    final layer = calloc<Uint8>(72);
    final name = '宇海断网保护'.toNativeUtf16();
    try {
      _writeGuid(layer, _sublayer);
      layer.cast<Uint64>()[2] = name.address;
      // weight 位于偏移 64，取最高，让这组规则先于系统防火墙被评估。
      layer.cast<Uint16>()[32] = 0xffff;
      return add(engine, layer, nullptr) == 0;
    } finally {
      calloc.free(name);
      calloc.free(layer);
    }
  }

  bool _addBlock(DynamicLibrary fwpu, int engine, List<int> layerKey) {
    return _addFilter(fwpu, engine, layerKey,
        weight: TrafficGuardPlan.blockWeight, action: _actionBlock);
  }

  bool _addAppPermit(
      DynamicLibrary fwpu, int engine, String path, List<int> layerKey) {
    final getId = fwpu.lookupFunction<
        Int32 Function(Pointer<Utf16>, Pointer<Pointer<Uint8>>),
        int Function(Pointer<Utf16>,
            Pointer<Pointer<Uint8>>)>('FwpmGetAppIdFromFileName0');
    final freeMem = fwpu.lookupFunction<Void Function(Pointer<Pointer<Void>>),
        void Function(Pointer<Pointer<Void>>)>('FwpmFreeMemory0');
    final file = path.toNativeUtf16();
    final blobOut = calloc<Pointer<Uint8>>();
    try {
      if (getId(file, blobOut) != 0 || blobOut.value == nullptr) return false;
      try {
        // 值是指向 FwpmGetAppIdFromFileName0 返回的 FWP_BYTE_BLOB 的指针。
        return _addPermit(fwpu, engine, layerKey, [
          _Condition(_appId, _byteBlob, blobOut.value.address),
        ]);
      } finally {
        final doomed = blobOut.cast<Pointer<Void>>();
        freeMem(doomed);
      }
    } finally {
      calloc.free(file);
      calloc.free(blobOut);
    }
  }

  /// 一条放行规则：所有条件同时满足才放行（不同字段之间是「且」）。
  /// FWPM_FILTER_CONDITION0 每条 40 字节：fieldKey 0–15，matchType 16，
  /// conditionValue.type 24，值 32（UINT8/16/32 直接放在这里，BLOB 放指针）。
  bool _addPermit(DynamicLibrary fwpu, int engine, List<int> layerKey,
      List<_Condition> conditions) {
    final array = calloc<Uint8>(40 * conditions.length);
    try {
      for (var i = 0; i < conditions.length; i++) {
        final c = array + 40 * i;
        final condition = conditions[i];
        _writeGuid(c, condition.field);
        c.cast<Uint32>()[4] = condition.match;
        c.cast<Uint32>()[6] = condition.type;
        c.cast<Uint64>()[4] = condition.value;
      }
      return _addFilter(fwpu, engine, layerKey,
          weight: TrafficGuardPlan.permitWeight,
          action: _actionPermit,
          condition: array,
          conditionCount: conditions.length);
    } finally {
      calloc.free(array);
    }
  }

  bool _addFilter(DynamicLibrary fwpu, int engine, List<int> layerKey,
      {required int weight,
      required int action,
      Pointer<Uint8>? condition,
      int conditionCount = 0}) {
    final add = fwpu.lookupFunction<
        Int32 Function(IntPtr, Pointer<Uint8>, Pointer<Void>, Pointer<Uint64>),
        int Function(int, Pointer<Uint8>, Pointer<Void>,
            Pointer<Uint64>)>('FwpmFilterAdd0');
    // x64 上 FWPM_FILTER0 是 200 字节（末尾的 effectiveWeight 在 184–199）。
    final filter = calloc<Uint8>(200);
    final name = '宇海'.toNativeUtf16();
    final id = calloc<Uint64>();
    try {
      filter.cast<Uint64>()[2] = name.address;
      _writeGuid(filter + 64, layerKey);
      _writeGuid(filter + 80, _sublayer);
      filter[96] = 1; // FWP_UINT8
      filter[104] = weight;
      if (condition != null && conditionCount > 0) {
        filter.cast<Uint32>()[28] = conditionCount; // numFilterConditions，偏移 112
        filter.cast<Uint64>()[15] = condition.address; // filterCondition，偏移 120
      }
      filter.cast<Uint32>()[32] = action;
      return add(engine, filter, nullptr, id) == 0;
    } finally {
      calloc.free(name);
      calloc.free(filter);
      calloc.free(id);
    }
  }
}

class _Condition {
  const _Condition(this.field, this.type, this.value, {this.match = 0});
  final List<int> field;
  final int type;
  final int value;

  /// 默认 FWP_MATCH_EQUAL（0）。
  final int match;
}

void _writeGuid(Pointer<Uint8> target, List<int> bytes) {
  for (var i = 0; i < 16; i++) {
    target[i] = bytes[i];
  }
}

// 固定子层，便于以后按同一把钥匙清理。布局是小端 GUID。
const _sublayer = <int>[
  0x20,
  0x1a,
  0x3c,
  0x8f,
  0x7e,
  0x6b,
  0x5a,
  0x4d,
  0x9c,
  0x1e,
  0x2a,
  0x7b,
  0x6e,
  0x5d,
  0x4c,
  0x31
];
const _connectV4 = <int>[
  0xd1,
  0x57,
  0x8d,
  0xc3,
  0xa7,
  0x05,
  0x33,
  0x4c,
  0x90,
  0x4f,
  0x7f,
  0xbc,
  0xee,
  0xe6,
  0x0e,
  0x82
];
const _connectV6 = <int>[
  0x3b,
  0x39,
  0x72,
  0x4a,
  0x9f,
  0x31,
  0xbc,
  0x44,
  0x84,
  0xc3,
  0xba,
  0x54,
  0xdc,
  0xb3,
  0xb6,
  0xb4
];
const _appId = <int>[
  0x87,
  0x1e,
  0x8e,
  0xd7,
  0x44,
  0x86,
  0xa5,
  0x4e,
  0x94,
  0x37,
  0xd8,
  0x09,
  0xec,
  0xef,
  0xc9,
  0x71
];
// FWPM_CONDITION_FLAGS {632ce23b-5167-435c-86d7-e903684aa80c}
const _conditionFlags = <int>[
  0x3b,
  0xe2,
  0x2c,
  0x63,
  0x67,
  0x51,
  0x5c,
  0x43,
  0x86,
  0xd7,
  0xe9,
  0x03,
  0x68,
  0x4a,
  0xa8,
  0x0c
];
// 以上 GUID、下面的枚举值都和 WireGuard for Windows（tunnel/firewall/types_windows.go）
// 里经过偏移测试的定义逐字核对过。
// FWPM_CONDITION_IP_REMOTE_ADDRESS {b235ae9a-1d64-49b8-a44c-5ff3d9095045}
const _remoteAddress = <int>[
  0x9a,
  0xae,
  0x35,
  0xb2,
  0x64,
  0x1d,
  0xb8,
  0x49,
  0xa4,
  0x4c,
  0x5f,
  0xf3,
  0xd9,
  0x09,
  0x50,
  0x45
];
// FWPM_CONDITION_IP_REMOTE_PORT {c35a604d-d22b-4e1a-91b4-68f674ee674b}
const _remotePort = <int>[
  0x4d,
  0x60,
  0x5a,
  0xc3,
  0x2b,
  0xd2,
  0x1a,
  0x4e,
  0x91,
  0xb4,
  0x68,
  0xf6,
  0x74,
  0xee,
  0x67,
  0x4b
];
// FWPM_CONDITION_IP_LOCAL_PORT {0c1ba1af-5765-453f-af22-a8f791ac775b}
const _localPort = <int>[
  0xaf,
  0xa1,
  0x1b,
  0x0c,
  0x65,
  0x57,
  0x3f,
  0x45,
  0xaf,
  0x22,
  0xa8,
  0xf7,
  0x91,
  0xac,
  0x77,
  0x5b
];
// FWPM_CONDITION_IP_PROTOCOL {3971ef2b-623e-4f9a-8cb1-6e79b806b9a7}
const _ipProtocol = <int>[
  0x2b,
  0xef,
  0x71,
  0x39,
  0x3e,
  0x62,
  0x9a,
  0x4f,
  0x8c,
  0xb1,
  0x6e,
  0x79,
  0xb8,
  0x06,
  0xb9,
  0xa7
];
const _uint8 = 1; // FWP_UINT8
const _uint16 = 2; // FWP_UINT16
const _uint32 = 3; // FWP_UINT32
const _byteBlob = 12; // FWP_BYTE_BLOB_TYPE
const _matchFlagsAllSet = 6; // FWP_MATCH_FLAGS_ALL_SET
const _flagIsLoopback = 0x00000001; // FWP_CONDITION_FLAG_IS_LOOPBACK
/// 172.19.0.2，主机字节序（WFP 的 IPv4 条件值按主机字节序）。和 windowsTunDns 一致。
const _tunDnsV4 = 0xAC130002;
const _actionBlock = 0x1001;
const _actionPermit = 0x1002;
