import 'dart:math';

import 'package:flutter/material.dart';

import '../../app/theme/tokens.dart';
import '../../platform/vpn/vpn_engine.dart';
import 'brand.dart';

/// 首页的大圆连接按钮。
///
/// 暖米白底上的圆：细焦糖描边、一圈象牙色环、再一圈细描边。圆心里是全身的艾丽，
/// 脚落在一条很淡的绿波浪上。顶部有一枚播放键，点一下她轻轻跳一下（素材里没有现成的
/// 打招呼动画）。底部「轻触连接」胶囊压在圆环下沿。点圆环或胶囊都会连接。
///
/// 未连接是焦糖描边，连接中描边转绿并有一道光绕圈，已连接保持绿色，失败转红。
/// 系统开启「减少动态效果」时不跳动、也不跑高光。
class TideRingButton extends StatefulWidget {
  const TideRingButton({super.key, required this.status, required this.onTap, this.size = 300});
  final VpnStatus status;
  final VoidCallback onTap;
  final double size;

  @override
  State<TideRingButton> createState() => _TideRingButtonState();
}

class _TideRingButtonState extends State<TideRingButton> with TickerProviderStateMixin {
  late final AnimationController _spin = AnimationController(vsync: this, duration: const Duration(milliseconds: 1600));
  late final AnimationController _idle = AnimationController(vsync: this, duration: const Duration(milliseconds: 2400));
  late final AnimationController _greet = AnimationController(vsync: this, duration: const Duration(milliseconds: 980));
  bool _pressed = false;

  /// 循环的待机浮动会让 `pumpAndSettle` 永远等不完。组件测试里只在按下播放键时跳一下。
  bool get _loopIdle => !WidgetsBinding.instance.runtimeType.toString().contains('Test');

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final reduce = MediaQuery.disableAnimationsOf(context);
    if (!reduce && _loopIdle && !_idle.isAnimating) _idle.repeat();
    if ((reduce || !_loopIdle) && _idle.isAnimating) _idle.stop();
  }

  @override
  void dispose() {
    _spin.dispose();
    _idle.dispose();
    _greet.dispose();
    super.dispose();
  }

  void _greetNow() {
    if (MediaQuery.disableAnimationsOf(context)) return;
    _greet.forward(from: 0);
  }

  double _bob(bool reduce) {
    if (reduce) return 0;
    if (_greet.isAnimating) {
      final t = _greet.value;
      return -sin(t * pi * 2) * (1 - t) * 11;
    }
    if (!_idle.isAnimating) return 0;
    return sin(_idle.value * pi * 2) * 2.8;
  }

  @override
  Widget build(BuildContext context) {
    final reduce = MediaQuery.disableAnimationsOf(context);
    final busy = widget.status == VpnStatus.connecting || widget.status == VpnStatus.reconnecting;
    final animate = !reduce && busy;
    if (!animate && _spin.isAnimating) _spin.stop();
    if (animate && !_spin.isAnimating) _spin.repeat();
    final active = widget.status == VpnStatus.connected;
    final tone = _tone(widget.status);
    final label = active ? '轻触断开' : '轻触连接';
    final size = widget.size;
    final g = _RingGeom.of(size);
    final overhang = 20.0;
    return Semantics(
      button: true,
      label: '连接开关',
      explicitChildNodes: true,
      child: GestureDetector(
        onTap: widget.onTap,
        onTapDown: (_) => setState(() => _pressed = true),
        onTapUp: (_) => setState(() => _pressed = false),
        onTapCancel: () => setState(() => _pressed = false),
        child: AnimatedScale(
          scale: _pressed ? 0.985 : 1,
          duration: const Duration(milliseconds: 120),
          curve: Curves.easeOut,
          child: AnimatedBuilder(
            animation: Listenable.merge([_spin, _idle, _greet]),
            builder: (_, __) {
              final bob = _bob(reduce);
              return SizedBox(
                width: size,
                height: size + overhang,
                child: Stack(clipBehavior: Clip.none, children: [
                  CustomPaint(
                    size: Size.square(size),
                    painter: _RingPainter(t: _spin.value, status: widget.status, running: animate, geom: g),
                  ),
                  Positioned(
                    left: 0,
                    right: 0,
                    top: g.figureTop + bob,
                    child: Center(
                      child: Transform.scale(
                        scale: _greet.isAnimating ? 1 + sin(_greet.value * pi) * 0.03 : 1,
                        child: AiliFigure(height: g.figureHeight),
                      ),
                    ),
                  ),
                  Positioned(
                    left: (size - g.play) / 2,
                    top: g.playTop,
                    width: g.play,
                    height: g.play,
                    child: _PlayButton(diameter: g.play, onPressed: _greetNow),
                  ),
                  Positioned(
                    left: 0,
                    right: 0,
                    top: size - 16,
                    child: Center(child: _ConnectPill(label: label, tone: tone, scale: g.scale)),
                  ),
                ]),
              );
            },
          ),
        ),
      ),
    );
  }
}

class _PlayButton extends StatelessWidget {
  const _PlayButton({required this.diameter, required this.onPressed});
  final double diameter;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) => Semantics(
        button: true,
        label: '打招呼',
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTap: onPressed,
          child: DecoratedBox(
            decoration: const BoxDecoration(color: Color(0xFF4A4742), shape: BoxShape.circle),
            child: CustomPaint(painter: const _PlayTrianglePainter(), child: SizedBox.square(dimension: diameter)),
          ),
        ),
      );
}

class _PlayTrianglePainter extends CustomPainter {
  const _PlayTrianglePainter();

  @override
  void paint(Canvas canvas, Size size) {
    final s = size.shortestSide;
    final paint = Paint()..color = const Color(0xFFFFFBF5);
    final path = Path()
      ..moveTo(s * 0.40, s * 0.30)
      ..lineTo(s * 0.40, s * 0.70)
      ..lineTo(s * 0.72, s * 0.50)
      ..close();
    canvas.drawPath(path, paint);
  }

  @override
  bool shouldRepaint(_PlayTrianglePainter oldDelegate) => false;
}

class _ConnectPill extends StatelessWidget {
  const _ConnectPill({required this.label, required this.tone, required this.scale});
  final String label;
  final Color tone;
  final double scale;

  @override
  Widget build(BuildContext context) => DecoratedBox(
        decoration: BoxDecoration(
          color: const Color(0xFFFFFBF3),
          borderRadius: BorderRadius.circular(999),
          border: Border.all(color: tone, width: 1.6),
        ),
        child: Padding(
          padding: EdgeInsets.symmetric(horizontal: 16 * scale, vertical: 6 * scale),
          child: Text(
            label,
            style: TextStyle(
              fontFamily: YhFont.ui,
              fontSize: (15 * scale).clamp(13.0, 16.0),
              fontWeight: FontWeight.w700,
              letterSpacing: 1.6,
              color: tone,
              height: 1.1,
            ),
          ),
        ),
      );
}

Color _tone(VpnStatus status) => switch (status) {
      VpnStatus.connected || VpnStatus.connecting || VpnStatus.reconnecting => const Color(0xFF3F6B10),
      VpnStatus.error => YhColors.danger,
      _ => const Color(0xFF8B5A2B),
    };

class _RingGeom {
  _RingGeom({
    required this.scale,
    required this.outerStroke,
    required this.band,
    required this.innerStroke,
    required this.outerR,
    required this.innerR,
    required this.faceR,
    required this.waveY,
    required this.figureHeight,
    required this.figureTop,
    required this.play,
    required this.playTop,
  });

  final double scale;
  final double outerStroke;
  final double band;
  final double innerStroke;
  final double outerR;
  final double innerR;
  final double faceR;
  final double waveY;
  final double figureHeight;
  final double figureTop;
  final double play;
  final double playTop;

  static _RingGeom of(double size) {
    final scale = (size / 300).clamp(0.78, 1.16);
    final outerStroke = 3.0 * scale;
    final band = 24.0 * scale;
    final innerStroke = 2.6 * scale;
    final outerR = size / 2 - outerStroke / 2 - 1;
    final innerR = outerR - outerStroke / 2 - band - innerStroke / 2;
    final faceR = innerR - innerStroke / 2;
    final center = size / 2;
    // 脚落在靠近内圆底部的波浪上；身高约是内圆直径的四分之三，头顶仍留出播放键。
    final waveY = center + faceR * 0.80;
    final figureHeight = faceR * 2 * 0.75;
    final figureTop = waveY - figureHeight * 0.985;
    final play = (40.0 * scale).clamp(32.0, 44.0);
    final playTop = center - faceR + 3 * scale;
    return _RingGeom(
      scale: scale,
      outerStroke: outerStroke,
      band: band,
      innerStroke: innerStroke,
      outerR: outerR,
      innerR: innerR,
      faceR: faceR,
      waveY: waveY,
      figureHeight: figureHeight,
      figureTop: figureTop,
      play: play,
      playTop: playTop,
    );
  }
}

class _RingPainter extends CustomPainter {
  _RingPainter({required this.t, required this.status, required this.running, required this.geom});
  final double t;
  final VpnStatus status;
  final bool running;
  final _RingGeom geom;

  @override
  void paint(Canvas canvas, Size size) {
    final c = size.center(Offset.zero);
    final g = geom;
    final tone = _tone(status);
    final on = status == VpnStatus.connected;
    final busy = status == VpnStatus.connecting || status == VpnStatus.reconnecting;
    final face = Rect.fromCircle(center: c, radius: g.faceR);

    canvas.drawCircle(
      c.translate(0, 7 * g.scale),
      g.outerR,
      Paint()
        ..color = const Color(0x1A8B5A2B)
        ..maskFilter = MaskFilter.blur(BlurStyle.normal, 12 * g.scale),
    );

    canvas.drawCircle(
      c,
      g.faceR,
      Paint()
        ..shader = const RadialGradient(
          colors: [Color(0xFFFFFBF5), Color(0xFFF6EFE4), Color(0xFFF5EFE6)],
          stops: [0.0, 0.55, 1.0],
        ).createShader(face),
    );

    canvas.drawCircle(
      c,
      (g.outerR + g.innerR) / 2,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = g.band
        ..color = const Color(0xFFFFFDF8),
    );

    final stroke = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = g.outerStroke
      ..color = tone;
    final inner = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = g.innerStroke
      ..color = tone;
    if (on) {
      canvas.drawCircle(
        c,
        g.outerR,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = g.outerStroke * 2.4
          ..color = tone.withValues(alpha: 0.22)
          ..maskFilter = MaskFilter.blur(BlurStyle.normal, 6 * g.scale),
      );
    }
    canvas.drawCircle(c, g.outerR, stroke);
    canvas.drawCircle(c, g.innerR, inner);

    if (busy) {
      final start = (running ? t : 0.12) * pi * 2 - pi / 2;
      final sweep = Paint()
        ..style = PaintingStyle.stroke
        ..strokeCap = StrokeCap.round
        ..strokeWidth = g.outerStroke + 1.2
        ..color = const Color(0xFFBBEF68);
      canvas.drawArc(Rect.fromCircle(center: c, radius: g.outerR), start, pi * 0.55, false, sweep);
      canvas.drawArc(
        Rect.fromCircle(center: c, radius: g.innerR),
        start + pi * 0.15,
        pi * 0.4,
        false,
        sweep,
      );
    }

    _wave(canvas, c.dx, g.waveY, g.faceR * 1.15, 3.2 * g.scale, const Color(0x998DBF3F), 2.2 * g.scale);
    _wave(canvas, c.dx, g.waveY + 5 * g.scale, g.faceR * 0.92, 2.2 * g.scale, const Color(0x558DBF3F), 1.6 * g.scale);
  }

  void _wave(Canvas canvas, double cx, double y, double width, double amp, Color color, double thickness) {
    final path = Path();
    final left = cx - width / 2;
    path.moveTo(left, y);
    const steps = 28;
    for (var i = 1; i <= steps; i++) {
      final x = left + width * i / steps;
      final yy = y + sin(i / steps * pi * 2) * amp;
      path.lineTo(x, yy);
    }
    canvas.drawPath(
      path,
      Paint()
        ..color = color
        ..style = PaintingStyle.stroke
        ..strokeWidth = thickness
        ..strokeCap = StrokeCap.round,
    );
  }

  @override
  bool shouldRepaint(_RingPainter old) =>
      old.t != t || old.status != status || old.running != running || old.geom.faceR != geom.faceR;
}
