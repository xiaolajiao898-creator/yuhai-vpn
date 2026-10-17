import 'dart:math';

import 'package:flutter/material.dart';

import '../../app/theme/tokens.dart';
import '../../platform/vpn/vpn_engine.dart';

/// 潮汐环连接按钮（方案 9.5）：全 App 唯一的循环动画。
/// 未连接：静止礁色环；连接中：两条正弦波缓慢流动；已连接：荧色环 + 呼吸；错误：珊瑚色静止。
/// 系统开启「减少动态效果」时停止动画，只保留状态色。
class TideRingButton extends StatefulWidget {
  const TideRingButton(
      {super.key, required this.status, required this.onTap, this.size = 220});
  final VpnStatus status;
  final VoidCallback onTap;
  final double size;

  @override
  State<TideRingButton> createState() => _TideRingButtonState();
}

class _TideRingButtonState extends State<TideRingButton>
    with SingleTickerProviderStateMixin {
  late final AnimationController _c =
      AnimationController(vsync: this, duration: const Duration(seconds: 4))
        ..repeat();

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final reduce = MediaQuery.of(context).disableAnimations;
    final animate = !reduce &&
        (widget.status == VpnStatus.connecting ||
            widget.status == VpnStatus.reconnecting ||
            widget.status == VpnStatus.connected);
    if (!animate && _c.isAnimating) _c.stop();
    if (animate && !_c.isAnimating) _c.repeat();
    final active = widget.status == VpnStatus.connected;
    return Semantics(
      button: true,
      label: '连接开关',
      child: GestureDetector(
        onTap: widget.onTap,
        child: AnimatedBuilder(
          animation: _c,
          builder: (_, __) => CustomPaint(
            size: Size.square(widget.size),
            painter: _TidePainter(t: _c.value, status: widget.status),
            child: SizedBox.square(
              dimension: widget.size,
              child: Center(
                  child: Column(mainAxisSize: MainAxisSize.min, children: [
                Icon(Icons.power_settings_new_rounded,
                    size: 56,
                    color: active ? YhColors.glow : YhColors.moonlight),
                const SizedBox(height: 12),
                Text(active ? '轻触断开' : '轻触连接',
                    style: TextStyle(
                        fontSize: 13,
                        letterSpacing: 2,
                        color: active ? YhColors.glow : YhColors.haze)),
              ])),
            ),
          ),
        ),
      ),
    );
  }
}

class _TidePainter extends CustomPainter {
  _TidePainter({required this.t, required this.status});
  final double t;
  final VpnStatus status;

  @override
  void paint(Canvas canvas, Size size) {
    final center = size.center(Offset.zero);
    final r = size.width / 2 - 12;
    final color = switch (status) {
      VpnStatus.connected => YhColors.glow,
      VpnStatus.error => YhColors.coral,
      VpnStatus.connecting || VpnStatus.reconnecting => YhColors.moonlight,
      _ => YhColors.reef,
    };
    final base = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 2
      ..color = YhColors.reef;
    canvas.drawCircle(center, r, base);

    final animated = status == VpnStatus.connecting ||
        status == VpnStatus.reconnecting ||
        status == VpnStatus.connected;
    if (!animated) {
      canvas.drawCircle(center, r, base..color = color);
      return;
    }
    // 两条正弦波沿圆周流动，振幅随状态不同：连接中 6，已连接 3（呼吸感）
    final amp = status == VpnStatus.connected ? 3.0 : 6.0;
    for (final (phase, alpha) in [(0.0, 1.0), (pi, 0.45)]) {
      final path = Path();
      for (var i = 0; i <= 360; i++) {
        final a = i * pi / 180;
        final wave = sin(a * 3 + t * 2 * pi + phase) * amp;
        final p = center + Offset(cos(a), sin(a)) * (r + wave);
        i == 0 ? path.moveTo(p.dx, p.dy) : path.lineTo(p.dx, p.dy);
      }
      canvas.drawPath(
        path,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = 2
          ..color = color.withValues(alpha: alpha),
      );
    }
  }

  @override
  bool shouldRepaint(_TidePainter old) => old.t != t || old.status != status;
}
