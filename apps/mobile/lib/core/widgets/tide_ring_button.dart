import 'dart:math';

import 'package:flutter/material.dart';

import '../../app/theme/tokens.dart';
import '../../platform/vpn/vpn_engine.dart';
import 'brand.dart';

/// 首页的大圆连接按钮（「米白」改版）。
///
/// 一个很大的圆：外圈是有体积的圆柱形环（截面是圆的，像一只救生圈，不是细线），圆心是按下去的
/// 白色圆面和品牌三道波。
/// - 未连接：米灰色的环、白色圆面，静止。
/// - 连接中：环变成青柠，一道高光绕着环跑（全 App 唯一的循环动画）。
/// - 已连接：青柠色的环带一圈柔光，圆面泛青柠，静止。
/// - 出错：珊瑚色的环，静止。
/// 系统开启「减少动态效果」时连接中也不跑高光，只保留状态色。
class TideRingButton extends StatefulWidget {
  const TideRingButton({super.key, required this.status, required this.onTap, this.size = 268});
  final VpnStatus status;
  final VoidCallback onTap;
  final double size;

  @override
  State<TideRingButton> createState() => _TideRingButtonState();
}

class _TideRingButtonState extends State<TideRingButton> with SingleTickerProviderStateMixin {
  late final AnimationController _c = AnimationController(vsync: this, duration: const Duration(milliseconds: 1600));
  bool _pressed = false;

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final reduce = MediaQuery.of(context).disableAnimations;
    final busy = widget.status == VpnStatus.connecting || widget.status == VpnStatus.reconnecting;
    final animate = !reduce && busy;
    if (!animate && _c.isAnimating) _c.stop();
    if (animate && !_c.isAnimating) _c.repeat();
    final active = widget.status == VpnStatus.connected;
    final hintColor = switch (widget.status) {
      VpnStatus.connected => YhColors.accentInk,
      VpnStatus.connecting || VpnStatus.reconnecting => YhColors.flow,
      VpnStatus.error => YhColors.danger,
      _ => YhColors.sub,
    };
    final size = widget.size;
    return Semantics(
      button: true,
      label: '连接开关',
      child: GestureDetector(
        onTap: widget.onTap,
        onTapDown: (_) => setState(() => _pressed = true),
        onTapUp: (_) => setState(() => _pressed = false),
        onTapCancel: () => setState(() => _pressed = false),
        child: AnimatedScale(
          scale: _pressed ? 0.965 : 1,
          duration: const Duration(milliseconds: 120),
          curve: Curves.easeOut,
          child: AnimatedBuilder(
            animation: _c,
            builder: (_, __) => CustomPaint(
              size: Size.square(size),
              painter: _TorusPainter(t: _c.value, status: widget.status, running: animate),
              child: SizedBox.square(
                dimension: size,
                child: Align(
                  alignment: const Alignment(0, 0.40),
                  child: Text(active ? '轻触断开' : '轻触连接',
                      style: TextStyle(
                          fontFamily: YhFont.ui,
                          fontSize: (size * 0.05).clamp(12.0, 15.0),
                          fontWeight: FontWeight.w700,
                          letterSpacing: 2.4,
                          color: hintColor)),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// 环和圆面的颜色：亮面、本色、暗面。
typedef _Tone = ({Color light, Color base, Color dark});

class _TorusPainter extends CustomPainter {
  _TorusPainter({required this.t, required this.status, required this.running});
  final double t;
  final VpnStatus status;
  final bool running;

  static const _idle = (light: Color(0xFFFFFDF8), base: Color(0xFFDFD6C4), dark: Color(0xFFA2957A));
  static const _lime = (light: Color(0xFFF2FFD2), base: YhColors.lime, dark: Color(0xFF6E9C2B));
  static const _error = (light: Color(0xFFFFEDE8), base: Color(0xFFF0B2A6), dark: Color(0xFFC26F62));

  @override
  void paint(Canvas canvas, Size size) {
    final c = size.center(Offset.zero);
    final outer = size.width / 2 - size.width * 0.045; // 留出阴影和柔光的位置
    final tube = outer * 0.25; // 圆柱环的粗细（截面直径）
    final mid = outer - tube / 2;
    final inner = outer - tube;
    final busy = status == VpnStatus.connecting || status == VpnStatus.reconnecting;
    final on = status == VpnStatus.connected;
    final _Tone ring = switch (status) {
      VpnStatus.connected || VpnStatus.connecting || VpnStatus.reconnecting => _lime,
      VpnStatus.error => _error,
      _ => _idle,
    };

    // 1. 已连接：环外一圈青柠柔光
    if (on) {
      canvas.drawCircle(
        c,
        mid,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = tube * 1.9
          ..color = YhColors.lime.withValues(alpha: 0.55)
          ..maskFilter = MaskFilter.blur(BlurStyle.normal, tube * 0.9),
      );
    }

    // 2. 落在米白底上的投影（向下偏一点），让环「立」起来
    canvas.drawCircle(
      c.translate(0, tube * 0.42),
      mid,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = tube * 0.95
        ..color = const Color(0xFF5A4A2A).withValues(alpha: on ? 0.16 : 0.20)
        ..maskFilter = MaskFilter.blur(BlurStyle.normal, tube * 0.42),
    );

    // 3. 圆柱环本体：沿半径方向「暗-本色-亮-本色-暗」，截面看起来是圆的
    final ringRect = Rect.fromCircle(center: c, radius: outer);
    canvas.saveLayer(ringRect.inflate(4), Paint());
    final across = RadialGradient(
      radius: 0.5,
      colors: [ring.dark, ring.base, ring.light, ring.base, ring.dark],
      stops: [
        inner / outer,
        (inner + tube * 0.16) / outer,
        (inner + tube * 0.40) / outer,
        (inner + tube * 0.74) / outer,
        1.0,
      ],
    ).createShader(ringRect);
    canvas.drawCircle(
      c,
      mid,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = tube
        ..shader = across,
    );
    // 光从左上方来：左上亮、右下暗，只画在环上（srcATop）
    canvas.drawRect(
      ringRect,
      Paint()
        ..blendMode = BlendMode.srcATop
        ..shader = LinearGradient(
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
          colors: [
            Colors.white.withValues(alpha: 0.38),
            Colors.white.withValues(alpha: 0.0),
            const Color(0xFF3A2F18).withValues(alpha: 0.22),
          ],
          stops: const [0.0, 0.5, 1.0],
        ).createShader(ringRect),
    );
    canvas.restore();

    // 4. 镜面高光：左上方一小段亮弧
    canvas.drawArc(
      Rect.fromCircle(center: c, radius: mid - tube * 0.10),
      pi * 1.06,
      pi * 0.40,
      false,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeCap = StrokeCap.round
        ..strokeWidth = tube * 0.17
        ..color = Colors.white.withValues(alpha: 0.75)
        ..maskFilter = MaskFilter.blur(BlurStyle.normal, tube * 0.05),
    );

    // 5. 连接中：一道高光绕着环跑
    if (busy) {
      final start = (running ? t : 0.0) * 2 * pi - pi / 2;
      canvas.drawArc(
        Rect.fromCircle(center: c, radius: mid),
        start,
        pi * 0.55,
        false,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeCap = StrokeCap.round
          ..strokeWidth = tube * 0.42
          ..shader = SweepGradient(
            endAngle: pi * 0.55,
            colors: [Colors.white.withValues(alpha: 0), Colors.white.withValues(alpha: 0.85)],
            transform: GradientRotation(start),
          ).createShader(Rect.fromCircle(center: c, radius: mid))
          ..maskFilter = MaskFilter.blur(BlurStyle.normal, tube * 0.08),
      );
    }

    // 6. 圆面：比环低一点的白色按钮面，上沿有一圈内阴影
    final face = inner - tube * 0.16;
    final faceRect = Rect.fromCircle(center: c, radius: face);
    final _Tone faceTone = on
        ? (light: const Color(0xFFF8FFE8), base: const Color(0xFFE6F6C6), dark: const Color(0xFFCFE6A4))
        : busy
            ? (light: const Color(0xFFFBFFF2), base: const Color(0xFFEEF6DE), dark: const Color(0xFFDCE8C4))
            : status == VpnStatus.error
                ? (light: const Color(0xFFFFF7F5), base: const Color(0xFFF9E6E1), dark: const Color(0xFFEBCFC8))
                : (light: const Color(0xFFFFFEFB), base: const Color(0xFFF7F2E8), dark: const Color(0xFFE8E0D0));
    canvas.drawCircle(
      c,
      face,
      Paint()
        ..shader = RadialGradient(
          center: const Alignment(-0.35, -0.45),
          radius: 1.05,
          colors: [faceTone.light, faceTone.base, faceTone.dark],
          stops: const [0.0, 0.62, 1.0],
        ).createShader(faceRect),
    );
    // 内阴影：上沿暗一圈，像按进去的面
    canvas.save();
    canvas.clipPath(Path()..addOval(faceRect));
    canvas.drawCircle(
      c.translate(0, -tube * 0.22),
      face + tube * 0.10,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = tube * 0.36
        ..color = const Color(0xFF5A4A2A).withValues(alpha: 0.12)
        ..maskFilter = MaskFilter.blur(BlurStyle.normal, tube * 0.16),
    );
    canvas.restore();
    canvas.drawCircle(
      c,
      face,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1
        ..color = YhColors.hair,
    );

    // 7. 圆心的品牌三道波
    final waveColor = switch (status) {
      VpnStatus.connected => YhColors.accentInk,
      VpnStatus.connecting || VpnStatus.reconnecting => YhColors.flow,
      VpnStatus.error => YhColors.danger,
      _ => YhColors.ink,
    };
    final s = face * 0.024;
    paintBrandWaves(canvas, s, waveColor, offsetX: c.dx - 32 * s, offsetY: c.dy - 32 * s - face * 0.16);
  }

  @override
  bool shouldRepaint(_TorusPainter old) => old.t != t || old.status != status || old.running != running;
}
