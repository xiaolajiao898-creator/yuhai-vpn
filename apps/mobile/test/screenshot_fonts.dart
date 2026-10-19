import 'dart:io';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/theme/tokens.dart';

/// `flutter_test` draws every family with Ahem until [FontLoader.load] runs.
/// Declaring the faces in pubspec.yaml only affects the shipping app.
Future<void> loadScreenshotFonts() async {
  TestWidgetsFlutterBinding.ensureInitialized();
  if (YhFont.ui != 'NotoSansSC') {
    throw StateError('ThemeData family is ${YhFont.ui}, expected NotoSansSC');
  }

  final noto = FontLoader(YhFont.ui)
    ..addFont(rootBundle.load('assets/fonts/NotoSansSC-Regular.otf'))
    ..addFont(rootBundle.load('assets/fonts/NotoSansSC-Medium.otf'))
    ..addFont(rootBundle.load('assets/fonts/NotoSansSC-Bold.otf'));
  await noto.load();

  final material = FontLoader('MaterialIcons')
    ..addFont(_readFile(_materialIconsPath()));
  await material.load();

  final cupertinoBytes = _readFile(_cupertinoIconsPath());
  final cupertino = FontLoader('CupertinoIcons')..addFont(cupertinoBytes);
  await cupertino.load();
  // IconData from cupertino_icons sets fontPackage, so the engine family is prefixed.
  final packaged = FontLoader('packages/cupertino_icons/CupertinoIcons')
    ..addFont(_readFile(_cupertinoIconsPath()));
  await packaged.load();
}

Future<ByteData> _readFile(String path) async {
  final bytes = await File(path).readAsBytes();
  if (bytes.isEmpty) {
    throw StateError('font file is empty: $path');
  }
  return ByteData.sublistView(Uint8List.fromList(bytes));
}

String _materialIconsPath() {
  final candidates = <String>[];
  final root = Platform.environment['FLUTTER_ROOT'];
  if (root != null && root.isNotEmpty) {
    candidates.add('$root/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf');
  }
  var dir = File(Platform.resolvedExecutable).parent;
  for (var i = 0; i < 8; i++) {
    candidates.add('${dir.path}/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf');
    candidates.add('${dir.path}/artifacts/material_fonts/MaterialIcons-Regular.otf');
    final parent = dir.parent;
    if (parent.path == dir.path) break;
    dir = parent;
  }
  for (final path in candidates) {
    if (File(path).existsSync()) return path;
  }
  throw StateError(
    'MaterialIcons-Regular.otf not found. Set FLUTTER_ROOT. Looked in:\n${candidates.join('\n')}',
  );
}

String _cupertinoIconsPath() {
  final pubCache = Platform.environment['PUB_CACHE'] ??
      '${Platform.environment['HOME'] ?? ''}/.pub-cache';
  final hosted = Directory('$pubCache/hosted/pub.dev');
  if (hosted.existsSync()) {
    for (final entity in hosted.listSync()) {
      if (entity is! Directory || !entity.path.contains('cupertino_icons-')) continue;
      final file = File('${entity.path}/assets/CupertinoIcons.ttf');
      if (file.existsSync()) return file.path;
    }
  }
  throw StateError('CupertinoIcons.ttf not found under $hosted');
}
