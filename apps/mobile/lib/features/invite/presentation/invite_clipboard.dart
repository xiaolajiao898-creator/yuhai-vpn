import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

class InviteClipboard {
  Future<String?> readText() async =>
      (await Clipboard.getData(Clipboard.kTextPlain))?.text;
  Future<void> writeText(String value) =>
      Clipboard.setData(ClipboardData(text: value));
}

final inviteClipboardProvider =
    Provider<InviteClipboard>((_) => InviteClipboard());
