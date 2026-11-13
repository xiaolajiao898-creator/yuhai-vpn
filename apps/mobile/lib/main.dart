import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'app/app.dart';
import 'app/register_smoke.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  if (Platform.environment['YH_SMOKE_REGISTER'] == '1') {
    exit(await runRegisterSmoke());
  }
  runApp(const ProviderScope(child: YuhaiApp()));
}
