import 'dart:ffi';
import 'dart:io';

import 'package:ffi/ffi.dart';

/// 把子进程收进当前进程持有的 Job Object。
///
/// `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`：本进程退出或崩溃、最后一个 Job 句柄被系统
/// 关掉时，xray 会被一起结束。Wintun 网卡随创建它的进程消失，指向它的路由一并消失。
abstract interface class ProcessSupervisor {
  Future<void> adopt(int pid);
  Future<void> release();
}

class NoopProcessSupervisor implements ProcessSupervisor {
  @override
  Future<void> adopt(int pid) async {}

  @override
  Future<void> release() async {}
}

ProcessSupervisor defaultProcessSupervisor() => Platform.isWindows
    ? WindowsJobSupervisor()
    : NoopProcessSupervisor();

class WindowsJobSupervisor implements ProcessSupervisor {
  static const _killOnJobClose = 0x2000;
  static const _jobObjectExtendedLimitInformation = 9;
  static const _processSetQuota = 0x0100;
  static const _processTerminate = 0x0001;

  /// x64 上 `JOBOBJECT_EXTENDED_LIMIT_INFORMATION` 的长度，以及其中
  /// `BasicLimitInformation.LimitFlags` 的偏移（两个 LARGE_INTEGER 之后）。
  static const _extendedLimitBytes = 144;
  static const _limitFlagsOffset = 16;

  DynamicLibrary? _kernel;
  int? _job;
  late final int Function(Pointer<Void>, Pointer<Utf16>) _createJobObject;
  late final int Function(int, int, Pointer<Void>, int) _setInformation;
  late final int Function(int, int, int) _openProcess;
  late final int Function(int, int) _assignProcess;
  late final int Function(int) _closeHandle;

  void _ensure() {
    if (_job != null) return;
    final kernel = _kernel ??= DynamicLibrary.open('kernel32.dll');
    _createJobObject = kernel.lookupFunction<
        IntPtr Function(Pointer<Void>, Pointer<Utf16>),
        int Function(Pointer<Void>, Pointer<Utf16>)>('CreateJobObjectW');
    _setInformation = kernel.lookupFunction<
        Int32 Function(IntPtr, Int32, Pointer<Void>, Uint32),
        int Function(int, int, Pointer<Void>, int)>('SetInformationJobObject');
    _openProcess = kernel.lookupFunction<
        IntPtr Function(Uint32, Int32, Uint32),
        int Function(int, int, int)>('OpenProcess');
    _assignProcess = kernel.lookupFunction<Int32 Function(IntPtr, IntPtr),
        int Function(int, int)>('AssignProcessToJobObject');
    _closeHandle =
        kernel.lookupFunction<Int32 Function(IntPtr), int Function(int)>(
            'CloseHandle');
    // 默认句柄不可继承，xray 不会把自己留在 Job 里导致父进程退出后杀不掉。
    final job = _createJobObject(nullptr, nullptr);
    if (job == 0) throw StateError('无法创建进程作业');
    // 按 4 字节对齐分配，LimitFlags 在偏移 16。
    final info = calloc<Uint32>(_extendedLimitBytes ~/ 4);
    try {
      (info + (_limitFlagsOffset ~/ 4)).value = _killOnJobClose;
      final ok = _setInformation(job, _jobObjectExtendedLimitInformation,
          info.cast<Void>(), _extendedLimitBytes);
      if (ok == 0) {
        _closeHandle(job);
        throw StateError('无法设置进程作业');
      }
    } finally {
      calloc.free(info);
    }
    _job = job;
  }

  @override
  Future<void> adopt(int pid) async {
    if (pid <= 0) throw StateError('无效的子进程');
    _ensure();
    final process = _openProcess(_processSetQuota | _processTerminate, 0, pid);
    if (process == 0) throw StateError('无法打开子进程');
    try {
      if (_assignProcess(_job!, process) == 0) {
        throw StateError('无法把子进程纳入作业');
      }
    } finally {
      _closeHandle(process);
    }
  }

  @override
  Future<void> release() async {
    final job = _job;
    _job = null;
    if (job != null) _closeHandle(job);
  }
}
