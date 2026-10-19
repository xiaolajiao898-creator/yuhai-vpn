import 'package:flutter/material.dart';

import '../../../app/theme/tokens.dart';

enum BrandScene { noContact, recover, connect, wallet }

/// 品牌插画：青柠底上的三道波，加上每个场景自己的图形。不使用照片素材。
class BrandSceneArt extends StatelessWidget {
  const BrandSceneArt({super.key, required this.scene, this.compact = false});
  final BrandScene scene;
  final bool compact;

  @override
  Widget build(BuildContext context) {
    final height = compact ? 112.0 : 168.0;
    return Semantics(
      label: switch (scene) {
        BrandScene.noContact => '不需要手机号和邮箱',
        BrandScene.recover => '从钱包地址转一笔 TRX 找回密码',
        BrandScene.connect => '一键连接',
        BrandScene.wallet => '从 TronLink 或 imToken 复制地址',
      },
      child: Container(
        height: height,
        width: double.infinity,
        clipBehavior: Clip.antiAlias,
        decoration: BoxDecoration(
          color: YhColors.paper,
          borderRadius: BorderRadius.circular(YhRadius.surface),
          border: Border.all(color: YhColors.line),
        ),
        child: CustomPaint(painter: _ScenePainter(scene)),
      ),
    );
  }
}

class _ScenePainter extends CustomPainter {
  _ScenePainter(this.scene);
  final BrandScene scene;

  @override
  void paint(Canvas canvas, Size size) {
    final mark = RRect.fromRectAndRadius(
      Rect.fromLTWH(20, size.height / 2 - 28, 56, 56),
      const Radius.circular(16),
    );
    canvas.drawRRect(mark, Paint()..color = YhColors.accent);
    final wave = Paint()
      ..color = YhColors.buttonInk
      ..style = PaintingStyle.stroke
      ..strokeWidth = 2.4
      ..strokeCap = StrokeCap.round;
    for (var i = 0; i < 3; i++) {
      final y = size.height / 2 - 12 + i * 10;
      final path = Path()
        ..moveTo(30, y)
        ..quadraticBezierTo(40, y - 7, 48, y)
        ..quadraticBezierTo(56, y + 7, 66, y);
      canvas.drawPath(path, wave);
    }
    final sub = Paint()..color = YhColors.sub;
    final accent = Paint()..color = YhColors.accent;
    final left = 96.0;
    switch (scene) {
      case BrandScene.noContact:
        canvas.drawRRect(
            RRect.fromRectAndRadius(Rect.fromLTWH(left, 36, size.width - left - 24, 28), const Radius.circular(8)),
            Paint()..color = YhColors.soft);
        _text(canvas, 'T… 你的地址', Offset(left + 12, 54), YhColors.ink);
        canvas.drawCircle(Offset(left + 18, 92), 8, sub);
        canvas.drawCircle(Offset(left + 44, 92), 8, accent);
        _text(canvas, '不要手机号', Offset(left + 60, 96), YhColors.sub);
      case BrandScene.wallet:
        _card(canvas, Rect.fromLTWH(left, 28, 120, 72));
        _text(canvas, 'TronLink', Offset(left + 14, 52), YhColors.ink);
        _text(canvas, '复制', Offset(left + 14, 76), YhColors.accent);
        _card(canvas, Rect.fromLTWH(left + 108, 48, 120, 72));
        _text(canvas, 'imToken', Offset(left + 122, 72), YhColors.ink);
        _text(canvas, '复制', Offset(left + 122, 96), YhColors.accent);
      case BrandScene.recover:
        _text(canvas, '你的地址', Offset(left, 48), YhColors.ink);
        _arrow(canvas, Offset(left + 70, size.height / 2), Offset(left + 130, size.height / 2));
        _text(canvas, '约 1 TRX', Offset(left + 78, size.height / 2 - 16), YhColors.accent);
        _text(canvas, '官方地址', Offset(left + 140, 48), YhColors.ink);
        _text(canvas, '链上确认后重设密码', Offset(left, size.height - 28), YhColors.sub);
      case BrandScene.connect:
        final c = Offset(left + 70, size.height / 2);
        canvas.drawCircle(c, 36, Paint()..color = YhColors.soft);
        canvas.drawCircle(
            c,
            36,
            Paint()
              ..style = PaintingStyle.stroke
              ..strokeWidth = 3
              ..color = YhColors.accent);
        canvas.drawCircle(c, 6, accent);
        canvas.drawLine(Offset(c.dx, c.dy - 16), Offset(c.dx, c.dy), wave);
        _text(canvas, '轻触连接', Offset(c.dx + 48, c.dy + 4), YhColors.ink);
    }
  }

  void _card(Canvas canvas, Rect rect) {
    canvas.drawRRect(
        RRect.fromRectAndRadius(rect, const Radius.circular(12)),
        Paint()..color = YhColors.soft);
  }

  void _arrow(Canvas canvas, Offset a, Offset b) {
    final paint = Paint()
      ..color = YhColors.accent
      ..strokeWidth = 2
      ..style = PaintingStyle.stroke;
    canvas.drawLine(a, b, paint);
    canvas.drawLine(b, b + const Offset(-8, -5), paint);
    canvas.drawLine(b, b + const Offset(-8, 5), paint);
  }

  void _text(Canvas canvas, String value, Offset at, Color color) {
    final painter = TextPainter(
      text: TextSpan(text: value, style: TextStyle(color: color, fontSize: 12, fontWeight: FontWeight.w600)),
      textDirection: TextDirection.ltr,
    )..layout();
    painter.paint(canvas, at - Offset(0, painter.height / 2));
  }

  @override
  bool shouldRepaint(_ScenePainter old) => old.scene != scene;
}
