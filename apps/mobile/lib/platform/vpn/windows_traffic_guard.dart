import 'dart:ffi';
import 'dart:io';

import 'package:ffi/ffi.dart';

/// 一条允许通过的进程，以及拦住其他所有 IPv4/IPv6 出站连接的计划。
class TrafficGuardPlan {
  const TrafficGuardPlan({required this.permitPaths, required this.blockAllOthers});
  final List<String> permitPaths;
  final bool blockAllOthers;

  static const permitWeight = 15;
  static const blockWeight = 1;
}

/// Windows 断网保护。
///
/// 用动态 WFP 会话拦住除本程序和 xray.exe 以外的出站连接（含 IPv6 和系统 DNS）。
/// 会话句柄随进程结束而关闭，过滤规则一起消失，所以崩溃不会把电脑的网络永久掐死。
/// 规则没有装上时必须返回 false，界面不能说「已保护」。
class WindowsTrafficGuard {
  WindowsTrafficGuard({DynamicLibrary? library}) : _library = library;

  final DynamicLibrary? _library;
  int? _engine;
  DynamicLibrary? _opened;

  bool get held => _engine != null;

  Future<bool> hold(List<String> permitPaths) async {
    if (!Platform.isWindows) return false;
    final paths = permitPaths.where((path) => path.isNotEmpty).toSet().toList();
    if (paths.isEmpty) return false;
    try {
      release();
      final fwpu = _opened = _library ?? DynamicLibrary.open('fwpuclnt.dll');
      final engine = calloc<IntPtr>();
      final session = calloc<Uint8>(72);
      try {
        // FWPM_SESSION_FLAG_DYNAMIC：进程退出、句柄关闭后规则消失。
        session[32] = 1;
        final open = fwpu.lookupFunction<
            Int32 Function(Pointer<Utf16>, Uint32, Pointer<Void>, Pointer<Uint8>, Pointer<IntPtr>),
            int Function(Pointer<Utf16>, int, Pointer<Void>, Pointer<Uint8>, Pointer<IntPtr>)>('FwpmEngineOpen0');
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
        final close = fwpu.lookupFunction<Int32 Function(IntPtr), int Function(int)>('FwpmEngineClose0');
        close(engine);
      }
    } catch (_) {}
  }

  bool _install(DynamicLibrary fwpu, int engine, List<String> paths) {
    final begin = fwpu.lookupFunction<Int32 Function(IntPtr, Uint32), int Function(int, int)>('FwpmTransactionBegin0');
    final commit = fwpu.lookupFunction<Int32 Function(IntPtr), int Function(int)>('FwpmTransactionCommit0');
    final abort = fwpu.lookupFunction<Int32 Function(IntPtr), int Function(int)>('FwpmTransactionAbort0');
    if (begin(engine, 0) != 0) return false;
    var ok = _addSublayer(fwpu, engine);
    if (ok) {
      ok = _addBlock(fwpu, engine, _connectV4) && _addBlock(fwpu, engine, _connectV6);
    }
    for (final path in paths) {
      if (!ok) break;
      ok = _addPermit(fwpu, engine, path, _connectV4) && _addPermit(fwpu, engine, path, _connectV6);
    }
    if (!ok) {
      abort(engine);
      return false;
    }
    return commit(engine) == 0;
  }

  bool _addSublayer(DynamicLibrary fwpu, int engine) {
    final add = fwpu.lookupFunction<Int32 Function(IntPtr, Pointer<Uint8>, Pointer<Void>), int Function(int, Pointer<Uint8>, Pointer<Void>)>('FwpmSubLayerAdd0');
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
    return _addFilter(fwpu, engine, layerKey, weight: TrafficGuardPlan.blockWeight, action: _actionBlock);
  }

  bool _addPermit(DynamicLibrary fwpu, int engine, String path, List<int> layerKey) {
    final getId = fwpu.lookupFunction<Int32 Function(Pointer<Utf16>, Pointer<Pointer<Uint8>>), int Function(Pointer<Utf16>, Pointer<Pointer<Uint8>>)>('FwpmGetAppIdFromFileName0');
    final freeMem = fwpu.lookupFunction<Void Function(Pointer<Pointer<Void>>), void Function(Pointer<Pointer<Void>>)>('FwpmFreeMemory0');
    final file = path.toNativeUtf16();
    final blobOut = calloc<Pointer<Uint8>>();
    try {
      if (getId(file, blobOut) != 0 || blobOut.value == nullptr) return false;
      final condition = calloc<Uint8>(40);
      try {
        _writeGuid(condition, _appId);
        // FWP_BYTE_BLOB_TYPE = 12，值指向 FwpmGetAppIdFromFileName0 返回的 blob。
        condition[20] = 12;
        condition.cast<Uint64>()[3] = blobOut.value.address;
        return _addFilter(fwpu, engine, layerKey,
            weight: TrafficGuardPlan.permitWeight,
            action: _actionPermit,
            condition: condition);
      } finally {
        calloc.free(condition);
        final doomed = blobOut.cast<Pointer<Void>>();
        freeMem(doomed);
      }
    } finally {
      calloc.free(file);
      calloc.free(blobOut);
    }
  }

  bool _addFilter(DynamicLibrary fwpu, int engine, List<int> layerKey,
      {required int weight, required int action, Pointer<Uint8>? condition}) {
    final add = fwpu.lookupFunction<Int32 Function(IntPtr, Pointer<Uint8>, Pointer<Void>, Pointer<Uint64>), int Function(int, Pointer<Uint8>, Pointer<Void>, Pointer<Uint64>)>('FwpmFilterAdd0');
    final filter = calloc<Uint8>(192);
    final name = '宇海'.toNativeUtf16();
    final id = calloc<Uint64>();
    try {
      filter.cast<Uint64>()[2] = name.address;
      _writeGuid(filter.elementAt(64), layerKey);
      _writeGuid(filter.elementAt(80), _sublayer);
      filter[96] = 1; // FWP_UINT8
      filter[104] = weight;
      if (condition != null) {
        filter.cast<Uint32>()[28] = 1;
        filter.cast<Uint64>()[15] = condition.address;
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

void _writeGuid(Pointer<Uint8> target, List<int> bytes) {
  for (var i = 0; i < 16; i++) {
    target[i] = bytes[i];
  }
}

// 固定子层，便于以后按同一把钥匙清理。布局是小端 GUID。
const _sublayer = <int>[0x20, 0x1a, 0x3c, 0x8f, 0x7e, 0x6b, 0x5a, 0x4d, 0x9c, 0x1e, 0x2a, 0x7b, 0x6e, 0x5d, 0x4c, 0x31];
const _connectV4 = <int>[0xd1, 0x57, 0x8d, 0xc3, 0xa7, 0x05, 0x33, 0x4c, 0x90, 0x4f, 0x7f, 0xbc, 0xee, 0xe6, 0x0e, 0x82];
const _connectV6 = <int>[0x3b, 0x39, 0x72, 0x4a, 0x9f, 0x31, 0xbc, 0x44, 0x84, 0xc3, 0xba, 0x54, 0xdc, 0xb3, 0xb6, 0xb4];
const _appId = <int>[0x87, 0x1e, 0x8e, 0xd7, 0x44, 0x86, 0xa5, 0x4e, 0x94, 0x37, 0xd8, 0x09, 0xec, 0xef, 0xc9, 0x71];
const _actionBlock = 0x1001;
const _actionPermit = 0x1002;
