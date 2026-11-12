import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../core/widgets/brand.dart';
import 'theme/tokens.dart';

/// 电脑上（Windows / macOS）把窗口拉宽或最大化时，整个界面排在居中、最宽 [maxWidth] 的一栏里。
///
/// 页面都是按手机宽度设计的。窗口一放大，所有页面（含弹窗、底部面板、提示条）都在中间这一栏里；
/// 栏内 MediaQuery 的宽度也改成栏宽，各页面照手机宽度排版，和前端演示（preview_main.dart）的做法一致。
///
/// 「夜潮」改版：栏外不再是一片底色，而是品牌底图（微光、潮汐线、颗粒）加上左右两侧的品牌排版，
/// 中间这一栏左右各一条细线，像一块立在海面上的屏幕。底图只是装饰，不响应点击。
///
/// 窗口不比 [maxWidth] 宽时（手机、默认大小的电脑窗口）栏宽就是窗口宽，排版不变。
/// 不论宽窄都包同样的几层，跨过 [maxWidth] 改窗口大小时页面状态不会丢。
class DesktopFrame extends StatelessWidget {
  const DesktopFrame({
    super.key,
    required this.child,
    this.enabled = true,
    this.maxWidth = defaultMaxWidth,
  });

  /// 和前端演示里的手机宽度一致。
  static const double defaultMaxWidth = 480;

  final Widget child;

  /// 只在电脑上打开。手机、平板不受影响。
  final bool enabled;

  final double maxWidth;

  @override
  Widget build(BuildContext context) {
    if (!enabled) return child;
    return LayoutBuilder(builder: (context, constraints) {
      final media = MediaQuery.of(context);
      final width = constraints.hasBoundedWidth
          ? math.min(constraints.maxWidth, maxWidth)
          : maxWidth;
      final height = constraints.hasBoundedHeight
          ? constraints.maxHeight
          : media.size.height;
      final size = Size(width, height);
      final full = constraints.hasBoundedWidth ? constraints.maxWidth : width;
      final margin = math.max(0.0, (full - width) / 2);
      final wide = margin >= 1;
      return Stack(
        fit: StackFit.expand,
        children: [
          Positioned.fill(
            child: IgnorePointer(
              child: ExcludeSemantics(
                child: _Surround(margin: margin, column: width),
              ),
            ),
          ),
          Center(
            child: SizedBox.fromSize(
              size: size,
              child: DecoratedBox(
                position: DecorationPosition.foreground,
                decoration: BoxDecoration(
                  border: wide
                      ? const Border.symmetric(vertical: BorderSide(color: YhColors.hairStrong))
                      : null,
                ),
                child: MediaQuery(
                  data: media.copyWith(size: size),
                  child: child,
                ),
              ),
            ),
          ),
        ],
      );
    });
  }
}

/// 栏外的品牌底图。左侧大字主广告词，右侧品牌标和要点。边距不够时只画底图。
class _Surround extends StatelessWidget {
  const _Surround({required this.margin, required this.column});
  final double margin, column;

  @override
  Widget build(BuildContext context) {
    final showType = margin >= 260;
    final size = (margin * 0.13).clamp(34.0, 84.0);
    // 两侧不在任何页面（Material）里面，没有默认文字样式；不包一层的话，
    // 文字会套上 MaterialApp 的「缺少 Material」提示样式（黄色双下划线）。
    return TideBackdrop(
      linesTop: 0.7,
      child: !showType
          ? const SizedBox.expand()
          : Material(
              type: MaterialType.transparency,
              child: Stack(children: [
              Positioned(
                left: margin * 0.12,
                bottom: 72,
                width: margin * 0.8,
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  const Eyebrow('YUHAI VPN · 宇海'),
                  const SizedBox(height: 18),
                  Text('为宇海社区\n而生的VPN，', style: YhType.display(size, height: 1.02)),
                  const SizedBox(height: 10),
                  Container(
                    padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
                    decoration: BoxDecoration(color: YhColors.accent, borderRadius: BorderRadius.circular(8)),
                    child: Text('您的安全宇海守护！', style: YhType.display(size * 0.62, color: YhColors.buttonInk)),
                  ),
                ]),
              ),
              Positioned(
                right: margin * 0.12,
                top: 56,
                child: Column(crossAxisAlignment: CrossAxisAlignment.end, children: [
                  const BrandMark(size: 44),
                  const SizedBox(height: 18),
                  for (final line in const ['WIREGUARD', 'USDT · TRC20', '3 DEVICES', 'NO LOGS'])
                    Padding(
                      padding: const EdgeInsets.only(top: 8),
                      child: Text(line, style: YhType.latin(12, color: YhColors.sub, weight: FontWeight.w500, spacing: 2.4)),
                    ),
                ]),
              ),
              // 吉祥物艾丽站在右侧
              Positioned.fill(
                left: margin + column,
                child: LayoutBuilder(builder: (context, box) {
                  final height = (box.maxHeight * 0.8).clamp(0.0, margin * 2.4);
                  return Align(
                    alignment: const Alignment(0.05, 1),
                    child: Padding(
                      padding: const EdgeInsets.only(bottom: 28),
                      child: AiliFigure(height: height),
                    ),
                  );
                }),
              ),
            ]),
            ),
    );
  }
}
