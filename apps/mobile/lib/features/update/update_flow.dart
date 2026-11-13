import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:package_info_plus/package_info_plus.dart';

import '../../platform/vpn/vpn_engine.dart';
import 'update_download.dart';
import 'update_policy.dart';
import 'update_prompt.dart';

const updateChannel = MethodChannel('yuhai/update');

/// 启动 5 秒后查一次，之后每 12 小时再查。只在正式包里自动查，避免测试被定时器拖住。
class UpdateScheduler extends StatefulWidget {
  const UpdateScheduler({super.key, required this.child, this.engine});
  final Widget child;
  final VpnEngine? engine;

  @override
  State<UpdateScheduler> createState() => _UpdateSchedulerState();
}

class _UpdateSchedulerState extends State<UpdateScheduler> {
  @override
  void initState() {
    super.initState();
    if (!kReleaseMode) return;
    Future<void>.delayed(const Duration(seconds: 5), () {
      if (mounted) checkForUpdate(context, engine: widget.engine);
    });
    _timer = Stream.periodic(const Duration(hours: 12)).listen((_) {
      if (mounted) checkForUpdate(context, engine: widget.engine);
    });
  }

  StreamSubscription<void>? _timer;

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => widget.child;
}

Future<void> checkForUpdate(BuildContext context, {VpnEngine? engine, bool manual = false, HttpClient? client}) async {
  final info = await PackageInfo.fromPlatform();
  final local = int.tryParse(info.buildNumber) ?? 0;
  final platform = Platform.isAndroid ? 'android' : Platform.isWindows ? 'windows' : '';
  if (platform.isEmpty) {
    if (manual && context.mounted) {
      ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('这个系统还不能在应用里升级')));
    }
    return;
  }
  AppReleaseInfo? latest;
  try {
    latest = await fetchLatest(platform, client: client);
  } on UpdateRejected {
    if (manual && context.mounted) {
      ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('暂时无法检查更新')));
    }
    return;
  }
  if (!context.mounted || latest == null) return;
  final kind = decideUpdate(localBuild: local, latest: latest);
  if (kind == UpdateKind.current) {
    if (manual) {
      ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('已是最新版本')));
    }
    return;
  }
  final forced = kind == UpdateKind.forced;
  if (!context.mounted) return;
  await showDialog<void>(
    context: context,
    barrierDismissible: !forced,
    builder: (dialogContext) => PopScope(
      canPop: !forced,
      child: Dialog(
        child: UpdatePrompt(
          version: latest!.version,
          notes: latest.notes,
          forced: forced,
          onLater: forced ? null : () => Navigator.of(dialogContext).pop(),
          onUpgrade: () => _upgrade(dialogContext, latest!, engine),
        ),
      ),
    ),
  );
}

Future<AppReleaseInfo?> fetchLatest(String platform, {HttpClient? client}) async {
  final uri = Uri.parse('https://api.yhvpn.io/v1/app/latest?platform=$platform');
  if (!releaseUrlAllowed(uri)) throw const UpdateRejected('更新地址无效');
  final http = client ?? HttpClient();
  final own = client == null;
  try {
    final request = await http.getUrl(uri);
    request.followRedirects = false;
    final response = await request.close();
    final body = await response.transform(utf8.decoder).join();
    if (response.statusCode != 200) throw const UpdateRejected('暂时无法检查更新');
    return AppReleaseInfo.parse(jsonDecode(body));
  } finally {
    if (own) http.close(force: true);
  }
}

Future<void> _upgrade(BuildContext context, AppReleaseInfo latest, VpnEngine? engine) async {
  final messenger = ScaffoldMessenger.maybeOf(context);
  final progress = ValueNotifier<double>(0);
  showDialog<void>(
    context: context,
    barrierDismissible: false,
    builder: (_) => Dialog(child: ValueListenableBuilder<double>(
      valueListenable: progress,
      builder: (_, value, __) => UpdatePrompt(
        version: latest.version,
        notes: latest.notes,
        forced: true,
        progress: value,
        onUpgrade: () {},
      ),
    )),
  );
  try {
    final uri = Uri.parse(latest.url);
    final dir = await _updateDirectory();
    final name = releaseFileName(uri);
    final file = await downloadRelease(
      url: uri,
      sha256: latest.sha256,
      directory: dir,
      fileName: name,
      onProgress: (got, total) => progress.value = total == 0 ? 0 : got / total,
    );
    await engine?.disconnect();
    if (Platform.isWindows) {
      await Process.start(file.path, windowsInstallerArgs, mode: ProcessStartMode.detached);
    } else if (Platform.isAndroid) {
      await updateChannel.invokeMethod<void>('installApk', {'path': file.path, 'certSha256': latest.certSha256});
    }
  } on UpdateRejected catch (error) {
    messenger?.showSnackBar(SnackBar(content: Text(error.message)));
  } catch (_) {
    messenger?.showSnackBar(const SnackBar(content: Text('升级没有完成，请稍后再试')));
  } finally {
    progress.dispose();
    if (context.mounted) Navigator.of(context, rootNavigator: true).pop();
  }
}

Future<Directory> _updateDirectory() async {
  if (Platform.isWindows) {
    final local = Platform.environment['LOCALAPPDATA'];
    if (local != null && local.isNotEmpty) return Directory('$local\\YuhaiVPN\\updates');
  }
  if (Platform.isAndroid) {
    final path = await updateChannel.invokeMethod<String>('updateDir');
    if (path != null && path.isNotEmpty) return Directory(path);
  }
  return Directory('${Directory.systemTemp.path}${Platform.pathSeparator}YuhaiVPN${Platform.pathSeparator}updates');
}
