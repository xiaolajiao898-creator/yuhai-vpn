import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

import 'screenshot_fonts.dart';

/// Loads real faces before any golden. Without this, flutter_test draws Ahem.
Future<void> testExecutable(FutureOr<void> Function() testMain) async {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUpAll(() async {
    if (Platform.environment['YH_SCREENSHOTS'] == '1') {
      await loadScreenshotFonts();
    }
  });
  await testMain();
}
