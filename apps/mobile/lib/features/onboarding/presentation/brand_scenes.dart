import 'package:flutter/material.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/widgets/brand.dart';

enum BrandScene { noContact, recover, connect, wallet }

/// 品牌插画（「夜潮」改版）：深色面板上的潮汐线、品牌标，加上每个场景自己的图形。不使用照片素材。
class BrandSceneArt extends StatelessWidget {
  const BrandSceneArt({super.key, required this.scene, this.compact = false});
  final BrandScene scene;
  final bool compact;

  @override
  Widget build(BuildContext context) {
    final height = compact ? 112.0 : 196.0;
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
          border: Border.all(color: YhColors.hair),
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
    // 底：潮汐线 + 颗粒（只用 drawPath / drawRect，不画圆角矩形）
    paintTideLines(canvas, size, top: size.height * 0.72, strength: 0.8);
    paintGrain(canvas, size, density: 0.006);

    // 品牌标：青柠圆角方块 + 三道波
    final markSize = size.height < 140 ? 56.0 : 64.0;
    final markTop = size.height / 2 - markSize / 2;
    final mark = RRect.fromRectAndRadius(
      Rect.fromLTWH(20, markTop, markSize, markSize),
      Radius.circular(markSize * 0.26),
    );
    canvas.drawRRect(mark, Paint()..color = YhColors.accent);
    final s = markSize / 64;
    paintBrandWaves(canvas, s, YhColors.buttonInk, offsetX: 20, offsetY: markTop);

    final left = 20 + markSize + 20.0;
    switch (scene) {
      case BrandScene.noContact:
        // 一条「地址」输入框（描边路径），下面是被划掉的手机和邮箱
        final field = Path()
          ..addRRect(RRect.fromRectAndRadius(
              Rect.fromLTWH(left, size.height / 2 - 46, size.width - left - 20, 36), const Radius.circular(18)));
        canvas.drawPath(field, Paint()..color = YhColors.deep);
        canvas.drawPath(
            field,
            Paint()
              ..style = PaintingStyle.stroke
              ..color = YhColors.accentInk.withValues(alpha: 0.7));
        _text(canvas, 'T… 你的地址', Offset(left + 16, size.height / 2 - 28), YhColors.ink, 13);
        _crossed(canvas, Offset(left + 14, size.height / 2 + 26), Icons.smartphone_rounded);
        _crossed(canvas, Offset(left + 48, size.height / 2 + 26), Icons.mail_outline_rounded);
        _text(canvas, '不要手机号', Offset(left + 72, size.height / 2 + 26), YhColors.sub, 12);
      case BrandScene.wallet:
        // 两张卡片整体放进插画框：上下居中；框窄（手机）时第二张往左收。
        const cardW = 120.0, cardH = 72.0, drop = 20.0;
        final top = (size.height - cardH - drop) / 2;
        // 至少错开 76，不盖住第一张上的「TronLink」。
        final shift = (size.width - left - cardW - 16).clamp(76.0, 108.0);
        _card(canvas, Rect.fromLTWH(left, top, cardW, cardH));
        _text(canvas, 'TronLink', Offset(left + 14, top + 24), YhColors.ink, 12);
        _text(canvas, '复制', Offset(left + 14, top + 48), YhColors.accentInk, 12);
        _card(canvas, Rect.fromLTWH(left + shift, top + drop, cardW, cardH));
        _text(canvas, 'imToken', Offset(left + shift + 14, top + drop + 24), YhColors.ink, 12);
        _text(canvas, '复制', Offset(left + shift + 14, top + drop + 48), YhColors.accentInk, 12);
      case BrandScene.recover:
        final mid = size.height / 2;
        _text(canvas, '你的地址', Offset(left, mid - 34), YhColors.ink, 13);
        _text(canvas, '官方地址', Offset(size.width - 20 - 52, mid - 34), YhColors.ink, 13);
        _arrow(canvas, Offset(left, mid - 4), Offset(size.width - 22, mid - 4));
        _chip(canvas, Offset((left + size.width - 20) / 2, mid - 4), '约 1 TRX');
        _text(canvas, '链上确认后重设密码', Offset(left, mid + 34), YhColors.sub, 12);
      case BrandScene.connect:
        final c = Offset(left + 52, size.height / 2);
        for (final (r, a) in [(46.0, 0.16), (36.0, 0.32)]) {
          canvas.drawCircle(
              c,
              r,
              Paint()
                ..style = PaintingStyle.stroke
                ..strokeWidth = 1.2
                ..color = YhColors.limeDeep.withValues(alpha: a));
        }
        canvas.drawCircle(c, 26, Paint()..color = YhColors.accent);
        paintBrandWaves(canvas, 0.62, YhColors.buttonInk, offsetX: c.dx - 32 * 0.62, offsetY: c.dy - 32 * 0.62);
        _text(canvas, '轻触连接', Offset(c.dx + 64, c.dy - 9), YhColors.ink, 14);
        _text(canvas, '24 小时免费试用', Offset(c.dx + 64, c.dy + 13), YhColors.accentInk, 12);
    }
  }

  void _card(Canvas canvas, Rect rect) {
    canvas.drawRRect(RRect.fromRectAndRadius(rect, const Radius.circular(14)), Paint()..color = YhColors.soft);
  }

  void _chip(Canvas canvas, Offset center, String label) {
    final painter = _painter(label, YhColors.buttonInk, 12, FontWeight.w700);
    final rect = Rect.fromCenter(center: center, width: painter.width + 20, height: 24);
    canvas.drawPath(Path()..addRRect(RRect.fromRectAndRadius(rect, const Radius.circular(12))),
        Paint()..color = YhColors.accent);
    painter.paint(canvas, center - Offset(painter.width / 2, painter.height / 2));
  }

  void _crossed(Canvas canvas, Offset center, IconData icon) {
    final painter = TextPainter(
      text: TextSpan(
          text: String.fromCharCode(icon.codePoint),
          style: TextStyle(fontFamily: icon.fontFamily, package: icon.fontPackage, fontSize: 20, color: YhColors.sub)),
      textDirection: TextDirection.ltr,
    )..layout();
    painter.paint(canvas, center - Offset(painter.width / 2, painter.height / 2));
    canvas.drawLine(
        center + const Offset(-11, 11),
        center + const Offset(11, -11),
        Paint()
          ..color = YhColors.danger
          ..strokeWidth = 2
          ..strokeCap = StrokeCap.round);
  }

  void _arrow(Canvas canvas, Offset a, Offset b) {
    final paint = Paint()
      ..color = YhColors.accentInk
      ..strokeWidth = 1.6
      ..style = PaintingStyle.stroke
      ..strokeCap = StrokeCap.round;
    canvas.drawLine(a, b, paint);
    canvas.drawLine(b, b + const Offset(-8, -5), paint);
    canvas.drawLine(b, b + const Offset(-8, 5), paint);
  }

  TextPainter _painter(String value, Color color, double size, FontWeight weight) => TextPainter(
        text: TextSpan(
            text: value,
            style: TextStyle(
                color: color,
                fontSize: size,
                fontWeight: weight,
                fontFamily: YhFont.ui,
                fontFamilyFallback: YhFont.fallback)),
        textDirection: TextDirection.ltr,
      )..layout();

  void _text(Canvas canvas, String value, Offset at, Color color, double size) {
    final painter = _painter(value, color, size, FontWeight.w600);
    painter.paint(canvas, at - Offset(0, painter.height / 2));
  }

  @override
  bool shouldRepaint(_ScenePainter old) => old.scene != scene;
}
