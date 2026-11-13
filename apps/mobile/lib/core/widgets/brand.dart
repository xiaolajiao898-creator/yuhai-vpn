import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../app/theme/tokens.dart';

/// 品牌标：青柠圆角方块上的三道墨色波浪（和官网、yhcc.io 同一个图形）。
class BrandMark extends StatelessWidget {
  const BrandMark({super.key, this.size = 40});
  final double size;

  @override
  Widget build(BuildContext context) => SizedBox.square(
        dimension: size,
        child: const CustomPaint(painter: BrandMarkPainter()),
      );
}

class BrandMarkPainter extends CustomPainter {
  const BrandMarkPainter({this.fill = YhColors.lime, this.wave = YhColors.buttonInk, this.withTile = true});
  final Color fill, wave;
  final bool withTile;

  @override
  void paint(Canvas canvas, Size size) {
    final s = size.shortestSide / 64;
    if (withTile) {
      canvas.drawRRect(
        RRect.fromRectAndRadius(Offset.zero & Size.square(64 * s), Radius.circular(16 * s)),
        Paint()..color = fill,
      );
    }
    paintBrandWaves(canvas, s, wave);
  }

  @override
  bool shouldRepaint(BrandMarkPainter old) => old.fill != fill || old.wave != wave || old.withTile != withTile;
}

/// 画三道波浪（64×64 坐标系里的 M13 22 q9-8 19 0 t19 0，乘以 [scale]）。
void paintBrandWaves(Canvas canvas, double scale, Color color, {double offsetX = 0, double offsetY = 0}) {
  final paint = Paint()
    ..color = color
    ..style = PaintingStyle.stroke
    ..strokeWidth = 4 * scale
    ..strokeCap = StrokeCap.round;
  for (final y in [22.0, 32.0, 42.0]) {
    final path = Path()
      ..moveTo(offsetX + 13 * scale, offsetY + y * scale)
      ..quadraticBezierTo(offsetX + 22 * scale, offsetY + (y - 8) * scale, offsetX + 32 * scale, offsetY + y * scale)
      ..quadraticBezierTo(offsetX + 42 * scale, offsetY + (y + 8) * scale, offsetX + 51 * scale, offsetY + y * scale);
    canvas.drawPath(path, paint);
  }
}

/// 页面底图：米白底，左下角青柠微光、右侧暖沙微光、横穿的三条潮汐线，再压一层纸面颗粒。
/// 静态绘制（不做循环动画），首页闲置时也不会一直刷新。
class TideBackdrop extends StatelessWidget {
  const TideBackdrop({super.key, required this.child, this.lines = true, this.linesTop = 0.58, this.glow = 1});
  final Widget child;
  final bool lines;

  /// 潮汐线的竖直位置（占高度的比例）。
  final double linesTop;

  /// 微光强度 0–1。
  final double glow;

  @override
  Widget build(BuildContext context) => Stack(fit: StackFit.passthrough, children: [
        Positioned.fill(
          child: RepaintBoundary(
            child: CustomPaint(
              isComplex: true,
              painter: _BackdropPainter(lines: lines, linesTop: linesTop, glow: glow),
            ),
          ),
        ),
        child,
      ]);
}

class _BackdropPainter extends CustomPainter {
  _BackdropPainter({required this.lines, required this.linesTop, required this.glow});
  final bool lines;
  final double linesTop, glow;

  @override
  void paint(Canvas canvas, Size size) {
    final rect = Offset.zero & size;
    canvas.drawRect(rect, Paint()..color = YhColors.bg);
    if (glow > 0) {
      canvas.drawRect(
        rect,
        Paint()
          ..shader = RadialGradient(
            center: const Alignment(-1.1, 1.15),
            radius: 1.25,
            colors: [YhColors.lime.withValues(alpha: 0.42 * glow), YhColors.lime.withValues(alpha: 0)],
          ).createShader(rect),
      );
      canvas.drawRect(
        rect,
        Paint()
          // 暖沙色微光放在右侧中段，顶部边缘保持纯底色，和标题栏接得上。
          ..shader = RadialGradient(
            center: const Alignment(1.25, 0.05),
            radius: 1.05,
            colors: [const Color(0xFFF1D9B5).withValues(alpha: 0.55 * glow), const Color(0x00F1D9B5)],
          ).createShader(rect),
      );
    }
    if (lines) paintTideLines(canvas, size, top: size.height * linesTop);
    paintGrain(canvas, size);
  }

  @override
  bool shouldRepaint(_BackdropPainter old) =>
      old.lines != lines || old.linesTop != linesTop || old.glow != glow;
}

/// 三条横穿的潮汐线，振幅和透明度逐条减弱。
void paintTideLines(Canvas canvas, Size size, {required double top, Color color = YhColors.limeDeep, double strength = 1}) {
  final period = math.max(size.width * 0.62, 220.0);
  for (final (i, amp, alpha, width) in [
    (0, 30.0, 0.62, 1.6),
    (1, 24.0, 0.38, 1.3),
    (2, 18.0, 0.22, 1.1),
  ]) {
    final y = top + i * 46.0;
    final path = Path()..moveTo(-period / 2, y);
    var x = -period / 2;
    var up = true;
    while (x < size.width + period) {
      path.quadraticBezierTo(x + period / 4, y + (up ? -amp : amp), x + period / 2, y);
      x += period / 2;
      up = !up;
    }
    canvas.drawPath(
      path,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = width
        ..color = color.withValues(alpha: alpha * strength),
    );
  }
}

/// 纸面颗粒：固定种子的随机小点，每次画出来都一样。
void paintGrain(Canvas canvas, Size size, {double density = 0.0045}) {
  final random = math.Random(7);
  final count = (size.width * size.height * density).clamp(0, 6000).toInt();
  final light = Paint()..color = const Color(0x40FFFFFF);
  final dark = Paint()..color = const Color(0x0F5A4A2A);
  for (var i = 0; i < count; i++) {
    final dx = random.nextDouble() * size.width;
    final dy = random.nextDouble() * size.height;
    canvas.drawRect(Rect.fromLTWH(dx, dy, 1.2, 1.2), i.isEven ? light : dark);
  }
}

/// 小号英文标签：一小段横线 + 字距拉开的英文/中文，放在大标题上面。
class Eyebrow extends StatelessWidget {
  const Eyebrow(this.text, {super.key, this.color = YhColors.accentInk});
  final String text;
  final Color color;

  @override
  Widget build(BuildContext context) => Row(mainAxisSize: MainAxisSize.min, children: [
        Container(width: 22, height: 1, color: color),
        const SizedBox(width: 10),
        Flexible(child: Text(text, style: YhType.eyebrow(color: color))),
      ]);
}

/// 细描边深色面板。
class YhPanel extends StatelessWidget {
  const YhPanel({
    super.key,
    required this.child,
    this.padding = const EdgeInsets.all(YhSpace.panel),
    this.color = YhColors.paper,
    this.borderColor = YhColors.hair,
    this.radius = YhRadius.surface,
    this.onTap,
  });
  final Widget child;
  final EdgeInsetsGeometry padding;
  final Color color, borderColor;
  final double radius;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final shape = RoundedRectangleBorder(
      side: BorderSide(color: borderColor),
      borderRadius: BorderRadius.circular(radius),
    );
    final content = Padding(padding: padding, child: child);
    return Material(
      color: color,
      shape: shape,
      clipBehavior: Clip.antiAlias,
      child: onTap == null ? content : InkWell(onTap: onTap, child: content),
    );
  }
}

/// 底部导航：浮在页面上的胶囊，当前页是一颗青柠药丸。
class YhTabBar extends StatelessWidget {
  const YhTabBar({super.key, required this.index, required this.onSelect});
  final int index;
  final ValueChanged<int> onSelect;

  static const items = [
    (Icons.shield_outlined, Icons.shield_rounded, '连接'),
    (Icons.workspace_premium_outlined, Icons.workspace_premium_rounded, '套餐'),
    (Icons.card_giftcard_outlined, Icons.card_giftcard_rounded, '邀请'),
    (Icons.person_outline_rounded, Icons.person_rounded, '我的'),
  ];

  @override
  Widget build(BuildContext context) {
    return SafeArea(
      top: false,
      minimum: const EdgeInsets.fromLTRB(16, 0, 16, 14),
      child: Container(
        height: 66,
        padding: const EdgeInsets.all(7),
        decoration: BoxDecoration(
          color: YhColors.paper.withValues(alpha: 0.97),
          borderRadius: BorderRadius.circular(33),
          border: Border.all(color: YhColors.hair),
          boxShadow: const [BoxShadow(color: Color(0x2414201C), blurRadius: 28, offset: Offset(0, 10))],
        ),
        child: Row(children: [
          for (var i = 0; i < items.length; i++)
            Expanded(
              flex: i == index ? 5 : 4,
              child: _TabItem(
                icon: i == index ? items[i].$2 : items[i].$1,
                label: items[i].$3,
                selected: i == index,
                onTap: () => onSelect(i),
              ),
            ),
        ]),
      ),
    );
  }
}

class _TabItem extends StatelessWidget {
  const _TabItem({required this.icon, required this.label, required this.selected, required this.onTap});
  final IconData icon;
  final String label;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final color = selected ? YhColors.buttonInk : YhColors.sub;
    return Semantics(
      button: true,
      selected: selected,
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTap: onTap,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 220),
          curve: Curves.easeOutCubic,
          decoration: BoxDecoration(
            color: selected ? YhColors.lime : Colors.transparent,
            borderRadius: BorderRadius.circular(26),
          ),
          alignment: Alignment.center,
          child: FittedBox(
            fit: BoxFit.scaleDown,
            child: Row(mainAxisSize: MainAxisSize.min, children: [
              Icon(icon, size: 20, color: color),
              const SizedBox(width: 6),
              Text(label,
                  style: TextStyle(
                      fontFamily: YhFont.ui,
                      fontSize: 13,
                      fontWeight: selected ? FontWeight.w700 : FontWeight.w500,
                      color: color)),
            ]),
          ),
        ),
      ),
    );
  }
}

/// 反色品牌标：墨色方块上的青柠波浪，放在青柠色卡片上。
class BrandMarkInverse extends StatelessWidget {
  const BrandMarkInverse({super.key, this.size = 40});
  final double size;

  @override
  Widget build(BuildContext context) => SizedBox.square(
        dimension: size,
        child: const CustomPaint(painter: BrandMarkPainter(fill: YhColors.buttonInk, wave: YhColors.lime)),
      );
}

/// 吉祥物艾丽的全身像（透明底）。只是装饰图：不接收点击、不进读屏。
class AiliFigure extends StatelessWidget {
  const AiliFigure({super.key, required this.height});
  final double height;

  static const asset = 'assets/images/aili.webp';

  /// 原图宽高比（448×1678）。
  static const aspect = 448 / 1678;

  @override
  Widget build(BuildContext context) => IgnorePointer(
        child: ExcludeSemantics(
          child: Image.asset(
            asset,
            height: height,
            width: height * aspect,
            fit: BoxFit.contain,
            filterQuality: FilterQuality.medium,
            gaplessPlayback: true,
            errorBuilder: (_, __, ___) => SizedBox(width: height * aspect, height: height),
          ),
        ),
      );
}
