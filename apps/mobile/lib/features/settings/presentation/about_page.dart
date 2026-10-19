import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../../app/providers.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../data/app_info_api.dart';
import 'settings_widgets.dart';

class AboutPage extends ConsumerStatefulWidget {
  const AboutPage({super.key});
  @override
  ConsumerState<AboutPage> createState() => _AboutPageState();
}

class _AboutPageState extends ConsumerState<AboutPage> {
  bool _checking = false;
  String? _message;
  AppRelease? _release;
  Future<void> _check() async {
    if (_checking) return;
    setState(() {
      _checking = true;
      _message = null;
      _release = null;
    });
    try {
      final release = await ref.read(appInfoApiProvider).latest();
      if (!mounted) return;
      final current = int.parse(appVersion.split('+').last);
      setState(() {
        _release = release;
        _message = release.code > current
            ? '发现新版本 ${release.name}${release.forceUpdate ? ' · 请升级后使用' : ''}'
            : '当前已是最新版本';
      });
    } on ApiException catch (error) {
      if (mounted) {
        setState(() => _message =
            error.code == 40401 ? '尚未发布正式版本，暂时没有可用的安装包。' : '暂时无法检查更新，请稍后重试。');
      }
    } catch (_) {
      if (mounted) setState(() => _message = '暂时无法检查更新，请稍后重试。');
    } finally {
      if (mounted) setState(() => _checking = false);
    }
  }

  Future<void> _copy(String value) async {
    try {
      await Clipboard.setData(ClipboardData(text: value));
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(const SnackBar(content: Text('链接已复制')));
      }
    } catch (_) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(const SnackBar(content: Text('复制失败，请长按链接复制')));
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    const official = String.fromEnvironment('YH_OFFICIAL_URL');
    const privacy = String.fromEnvironment('YH_PRIVACY_URL');
    const terms = String.fromEnvironment('YH_TERMS_URL');
    final links = [
      ('官方网站', safeReleaseLink(official)),
      ('隐私政策', safeReleaseLink(privacy)),
      ('服务条款', safeReleaseLink(terms))
    ];
    return Scaffold(
      appBar: AppBar(title: const Text('关于宇海VPN')),
      body: ListView(padding: const EdgeInsets.all(YhSpace.page), children: [
        const SizedBox(height: 16),
        Center(
            child: Container(
                width: 88,
                height: 88,
                decoration: BoxDecoration(
                    color: YhColors.current,
                    borderRadius: BorderRadius.circular(28),
                    border: Border.all(color: YhColors.reef)),
                child: const Icon(Icons.shield_moon_outlined,
                    size: 44, color: YhColors.glow))),
        const SizedBox(height: 16),
        const Text('宇海VPN',
            textAlign: TextAlign.center,
            style: TextStyle(
                fontSize: 32,
                fontWeight: FontWeight.w600,
                fontFamily: YhFont.ui,
                letterSpacing: 0.4)),
        const SizedBox(height: 8),
        const Text('深海之中，自在连接',
            textAlign: TextAlign.center,
            style: TextStyle(color: YhColors.haze)),
        const SizedBox(height: 28),
        SettingsPanel(
            child: Padding(
                padding: const EdgeInsets.all(20),
                child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      const Text('当前版本',
                          style: TextStyle(color: YhColors.haze)),
                      const SizedBox(height: 6),
                      const Text(appVersion,
                          style: TextStyle(
                              fontSize: 26, fontWeight: FontWeight.w800)),
                      const SizedBox(height: 12),
                      const Text('开发演示版 · VPN 连接为模拟模式，尚未发布正式安装包。',
                          style: TextStyle(color: YhColors.starlight)),
                      const SizedBox(height: 16),
                      OutlinedButton.icon(
                          onPressed: _checking ? null : _check,
                          icon: const Icon(Icons.refresh),
                          label: Text(_checking ? '正在检查…' : '检查更新')),
                      if (_message != null)
                        Padding(
                            padding: const EdgeInsets.only(top: 12),
                            child: Text(_message!)),
                      if (_release?.notes.isNotEmpty ?? false)
                        Padding(
                            padding: const EdgeInsets.only(top: 8),
                            child: Text(_release!.notes)),
                      if (_release?.downloadUrl != null) ...[
                        const SizedBox(height: 12),
                        SelectableText(_release!.downloadUrl!),
                        TextButton.icon(
                            onPressed: () => _copy(_release!.downloadUrl!),
                            icon: const Icon(Icons.copy),
                            label: const Text('复制官方安装链接')),
                      ],
                    ]))),
        const SizedBox(height: 20),
        SettingsPanel(
            child: Column(children: [
          for (final link in links)
            SettingsRow(
                icon: link.$1 == '官方网站'
                    ? Icons.language
                    : Icons.description_outlined,
                title: link.$1,
                subtitle: link.$2 == null ? '正式发布前提供' : '点按复制 · ${link.$2}',
                onTap: link.$2 == null ? null : () => _copy(link.$2!)),
        ])),
        const SizedBox(height: 20),
        const Text('请保管好账户密钥。当前页面不会要求你提交账户密钥或钱包私钥。',
            style: TextStyle(color: YhColors.haze)),
      ]),
    );
  }
}
