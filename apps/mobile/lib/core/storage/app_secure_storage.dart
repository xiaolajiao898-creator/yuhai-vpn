import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Shared secure-storage options.
///
/// `resetOnError` stays false: a keystore glitch must not delete the session
/// and device id. Callers treat a read failure as "try again", not as logout.
const FlutterSecureStorage appSecureStorage = FlutterSecureStorage(
  aOptions: AndroidOptions(resetOnError: false),
  mOptions: MacOsOptions(useDataProtectionKeyChain: false),
);
