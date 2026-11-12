import 'dart:ui' show lerpDouble;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'tokens.dart';

/// 「米白」主题：暖米白底、白卡片、青柠胶囊按钮、深绿强调字、得意黑标题。
/// 函数名沿用 buildDeepSeaTheme，测试和入口都按这个名字取主题。
ThemeData buildDeepSeaTheme() {
  const scheme = ColorScheme.light(
    surface: YhColors.bg,
    onSurface: YhColors.ink,
    onSurfaceVariant: YhColors.sub,
    surfaceContainerLowest: YhColors.deep,
    surfaceContainerLow: YhColors.paper,
    surfaceContainer: YhColors.paper,
    surfaceContainerHigh: YhColors.soft,
    surfaceContainerHighest: YhColors.soft,
    primary: YhColors.accentInk,
    onPrimary: YhColors.paper,
    primaryContainer: YhColors.lime,
    onPrimaryContainer: YhColors.buttonInk,
    secondary: YhColors.flow,
    onSecondary: YhColors.paper,
    tertiary: YhColors.warn,
    error: YhColors.danger,
    onError: YhColors.paper,
    outline: YhColors.line,
    outlineVariant: YhColors.line,
    inverseSurface: YhColors.ink,
    onInverseSurface: YhColors.bg,
  );
  const body = TextStyle(
    color: YhColors.ink,
    height: YhFont.lineHeight,
    fontFamily: YhFont.ui,
    fontFamilyFallback: YhFont.fallback,
  );
  final display = YhType.display(40);
  final text = TextTheme(
    displayLarge: display.copyWith(fontSize: 52, height: 1.04),
    displayMedium: display.copyWith(fontSize: 44, height: 1.06),
    displaySmall: display.copyWith(fontSize: 36, height: 1.08),
    headlineLarge: display.copyWith(fontSize: 34, height: 1.1),
    headlineMedium: display.copyWith(fontSize: 30, height: 1.12),
    headlineSmall: display.copyWith(fontSize: 26, height: 1.15),
    titleLarge: display.copyWith(fontSize: 22, height: 1.25),
    titleMedium: body.copyWith(fontSize: 16, fontWeight: FontWeight.w600),
    titleSmall: body.copyWith(fontSize: 14, fontWeight: FontWeight.w600),
    bodyLarge: body.copyWith(fontSize: 16),
    bodyMedium: body.copyWith(fontSize: 14),
    bodySmall: body.copyWith(fontSize: 12, color: YhColors.sub),
    labelLarge: body.copyWith(fontSize: 15, fontWeight: FontWeight.w600, letterSpacing: 0.3),
    labelMedium: body.copyWith(fontSize: 13, color: YhColors.sub),
    labelSmall: body.copyWith(fontSize: 12, color: YhColors.haze, letterSpacing: 0.2),
  );
  const pill = StadiumBorder();
  const buttonText = TextStyle(
      fontSize: 16, fontWeight: FontWeight.w700, fontFamily: YhFont.ui, letterSpacing: 0.6);
  return ThemeData(
    useMaterial3: true,
    fontFamily: YhFont.ui,
    colorScheme: scheme,
    scaffoldBackgroundColor: YhColors.bg,
    canvasColor: YhColors.bg,
    textTheme: text,
    dividerColor: YhColors.line,
    dividerTheme: const DividerThemeData(color: YhColors.line, thickness: 1, space: 1),
    splashFactory: NoSplash.splashFactory, // 无水波：层级靠白卡、细线和青柠
    highlightColor: YhColors.hair,
    hoverColor: const Color(0x0A14201C),
    focusColor: const Color(0x4DBBEF68),
    iconTheme: const IconThemeData(color: YhColors.ink, size: 22),
    actionIconTheme: ActionIconThemeData(
      backButtonIconBuilder: (_) => const _BackGlyph(),
      closeButtonIconBuilder: (_) => const Icon(Icons.close_rounded, size: 22),
    ),
    bottomSheetTheme: const BottomSheetThemeData(
      backgroundColor: YhColors.paper,
      modalBackgroundColor: YhColors.paper,
      surfaceTintColor: Colors.transparent,
      showDragHandle: false,
      dragHandleColor: YhColors.hairLime,
      dragHandleSize: Size(44, 4),
      shape: RoundedRectangleBorder(
        side: BorderSide(color: YhColors.hairStrong),
        borderRadius: BorderRadius.vertical(top: Radius.circular(YhRadius.sheet)),
      ),
    ),
    dialogTheme: DialogThemeData(
      backgroundColor: YhColors.paper,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        side: const BorderSide(color: YhColors.hairStrong),
        borderRadius: BorderRadius.circular(YhRadius.sheet),
      ),
      titleTextStyle: YhType.display(26),
      contentTextStyle: body.copyWith(fontSize: 15, color: YhColors.sub),
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: YhColors.navy,
      contentTextStyle: body.copyWith(color: YhColors.bg, fontSize: 14, fontWeight: FontWeight.w600),
      actionTextColor: YhColors.lime,
      elevation: 0,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(18)),
    ),
    appBarTheme: AppBarTheme(
      backgroundColor: YhColors.bg,
      foregroundColor: YhColors.ink,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
      toolbarHeight: 64,
      titleSpacing: 20,
      titleTextStyle: YhType.display(26),
      systemOverlayStyle: SystemUiOverlayStyle.dark,
      iconTheme: const IconThemeData(color: YhColors.ink),
      actionsIconTheme: const IconThemeData(color: YhColors.ink),
    ),
    navigationBarTheme: NavigationBarThemeData(
      backgroundColor: YhColors.paper,
      indicatorColor: YhColors.accent,
      surfaceTintColor: Colors.transparent,
      iconTheme: WidgetStateProperty.resolveWith((states) => IconThemeData(
          color: states.contains(WidgetState.selected) ? YhColors.buttonInk : YhColors.sub)),
      labelTextStyle: WidgetStateProperty.resolveWith((states) => TextStyle(
          fontSize: 12,
          fontFamily: YhFont.ui,
          fontWeight: FontWeight.w600,
          color: states.contains(WidgetState.selected) ? YhColors.accentInk : YhColors.sub)),
    ),
    cardTheme: CardThemeData(
      color: YhColors.paper,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
          side: const BorderSide(color: YhColors.hair),
          borderRadius: BorderRadius.circular(YhRadius.surface)),
    ),
    listTileTheme: ListTileThemeData(
      iconColor: YhColors.sub,
      textColor: YhColors.ink,
      titleTextStyle: body.copyWith(fontSize: 16, fontWeight: FontWeight.w500),
      subtitleTextStyle: body.copyWith(fontSize: 12.5, color: YhColors.sub),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(YhRadius.listSelected)),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        backgroundColor: YhColors.button,
        foregroundColor: YhColors.buttonInk,
        // 不可点时换成中性灰，不再是发暗的荧光绿，一眼能看出「还不能点」。
        disabledBackgroundColor: YhColors.soft,
        disabledForegroundColor: YhColors.faint,
        minimumSize: const Size.fromHeight(56),
        padding: const EdgeInsets.symmetric(horizontal: 26),
        shape: pill,
        textStyle: buttonText,
      ),
    ),
    elevatedButtonTheme: ElevatedButtonThemeData(
      style: ElevatedButton.styleFrom(
        backgroundColor: YhColors.button,
        foregroundColor: YhColors.buttonInk,
        elevation: 0,
        minimumSize: const Size.fromHeight(56),
        shape: pill,
        textStyle: buttonText,
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        foregroundColor: YhColors.ink,
        minimumSize: const Size.fromHeight(52),
        padding: const EdgeInsets.symmetric(horizontal: 22),
        shape: pill,
        side: const BorderSide(color: YhColors.hairStrong),
        textStyle: buttonText.copyWith(fontSize: 15, fontWeight: FontWeight.w600),
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(
        foregroundColor: YhColors.accentInk,
        shape: pill,
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
        textStyle: buttonText.copyWith(fontSize: 14.5, fontWeight: FontWeight.w600, letterSpacing: 0.2),
      ),
    ),
    iconButtonTheme: IconButtonThemeData(
      style: IconButton.styleFrom(foregroundColor: YhColors.ink),
    ),
    switchTheme: SwitchThemeData(
      thumbColor: WidgetStateProperty.resolveWith((s) =>
          s.contains(WidgetState.selected) ? YhColors.buttonInk : YhColors.sub),
      trackColor: WidgetStateProperty.resolveWith((s) =>
          s.contains(WidgetState.selected) ? YhColors.accent : YhColors.soft),
      trackOutlineColor: WidgetStateProperty.resolveWith((s) =>
          s.contains(WidgetState.selected) ? YhColors.limeDeep : YhColors.hairStrong),
    ),
    checkboxTheme: CheckboxThemeData(
      fillColor: WidgetStateProperty.resolveWith((s) =>
          s.contains(WidgetState.selected) ? YhColors.accent : Colors.transparent),
      checkColor: const WidgetStatePropertyAll(YhColors.buttonInk),
      side: const BorderSide(color: YhColors.hairStrong, width: 1.5),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(6)),
    ),
    radioTheme: RadioThemeData(
      fillColor: WidgetStateProperty.resolveWith((s) =>
          s.contains(WidgetState.selected) ? YhColors.accentInk : YhColors.sub),
    ),
    progressIndicatorTheme: const ProgressIndicatorThemeData(
      color: YhColors.accentInk,
      linearTrackColor: YhColors.soft,
      circularTrackColor: Colors.transparent,
    ),
    chipTheme: ChipThemeData(
      backgroundColor: YhColors.soft,
      selectedColor: YhColors.accent,
      side: const BorderSide(color: YhColors.hair),
      shape: const StadiumBorder(),
      labelStyle: body.copyWith(fontSize: 13, fontWeight: FontWeight.w600),
      secondaryLabelStyle: body.copyWith(fontSize: 13, fontWeight: FontWeight.w600, color: YhColors.buttonInk),
      checkmarkColor: YhColors.buttonInk,
    ),
    segmentedButtonTheme: SegmentedButtonThemeData(
      style: ButtonStyle(
        backgroundColor: WidgetStateProperty.resolveWith((s) =>
            s.contains(WidgetState.selected) ? YhColors.accent : Colors.transparent),
        foregroundColor: WidgetStateProperty.resolveWith((s) =>
            s.contains(WidgetState.selected) ? YhColors.buttonInk : YhColors.ink),
        side: const WidgetStatePropertyAll(BorderSide(color: YhColors.hairStrong)),
        shape: const WidgetStatePropertyAll(StadiumBorder()),
      ),
    ),
    tooltipTheme: TooltipThemeData(
      decoration: BoxDecoration(color: YhColors.navy, borderRadius: BorderRadius.circular(10)),
      textStyle: body.copyWith(fontSize: 12, color: YhColors.bg),
    ),
    // 输入框：米白底上的白色圆角块、细描边、圆角 18，聚焦时深绿描边。标签留在框里（见 [YhFieldBorder]）。
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: YhColors.deep,
      floatingLabelBehavior: FloatingLabelBehavior.auto,
      contentPadding: const EdgeInsets.fromLTRB(18, 20, 18, 16),
      labelStyle: body.copyWith(fontSize: 15, color: YhColors.sub),
      floatingLabelStyle: WidgetStateTextStyle.resolveWith((s) => body.copyWith(
          fontSize: 13,
          fontWeight: FontWeight.w600,
          color: s.contains(WidgetState.error)
              ? YhColors.danger
              : s.contains(WidgetState.focused)
                  ? YhColors.accentInk
                  : YhColors.sub)),
      hintStyle: body.copyWith(fontSize: 15, color: YhColors.faint),
      helperStyle: body.copyWith(fontSize: 12, color: YhColors.faint),
      errorStyle: body.copyWith(fontSize: 12, color: YhColors.danger),
      prefixIconColor: YhColors.sub,
      suffixIconColor: YhColors.sub,
      border: const YhFieldBorder(),
      enabledBorder: const YhFieldBorder(),
      disabledBorder: const YhFieldBorder(borderSide: BorderSide(color: YhColors.hair)),
      focusedBorder: const YhFieldBorder(borderSide: BorderSide(color: YhColors.accentInk, width: 1.8)),
      errorBorder: const YhFieldBorder(borderSide: BorderSide(color: YhColors.danger)),
      focusedErrorBorder: const YhFieldBorder(borderSide: BorderSide(color: YhColors.danger, width: 1.6)),
    ),
    textSelectionTheme: const TextSelectionThemeData(
      cursorColor: YhColors.accentInk,
      selectionColor: Color(0x80BBEF68),
      selectionHandleColor: YhColors.accentInk,
    ),
    pageTransitionsTheme: const PageTransitionsTheme(builders: {
      TargetPlatform.android: _FadeSlideTransitions(),
      TargetPlatform.iOS: _FadeSlideTransitions(),
      TargetPlatform.windows: _FadeSlideTransitions(),
      TargetPlatform.macOS: _FadeSlideTransitions(),
      TargetPlatform.linux: _FadeSlideTransitions(),
    }),
  );
}

/// 返回键：细描边圆圈里的一支箭头。
class _BackGlyph extends StatelessWidget {
  const _BackGlyph();

  @override
  Widget build(BuildContext context) => Container(
        width: 38,
        height: 38,
        decoration: BoxDecoration(
          shape: BoxShape.circle,
          border: Border.all(color: YhColors.hairStrong),
        ),
        alignment: Alignment.center,
        child: const Icon(Icons.arrow_back_rounded, size: 19, color: YhColors.ink),
      );
}

/// 输入框边框：圆角矩形细线，但对 InputDecorator 来说「不是描边框」（[isOutline] 为 false），
/// 所以浮动标签留在框里面，不会像 OutlineInputBorder 那样骑在顶边上、把线切开。
class YhFieldBorder extends InputBorder {
  const YhFieldBorder({
    super.borderSide = const BorderSide(color: YhColors.hairStrong),
    this.radius = YhRadius.field,
  });

  final double radius;

  @override
  bool get isOutline => false;

  @override
  EdgeInsetsGeometry get dimensions => EdgeInsets.all(borderSide.width);

  @override
  YhFieldBorder copyWith({BorderSide? borderSide, double? radius}) =>
      YhFieldBorder(borderSide: borderSide ?? this.borderSide, radius: radius ?? this.radius);

  @override
  ShapeBorder scale(double t) => YhFieldBorder(borderSide: borderSide.scale(t), radius: radius * t);

  RRect _shape(Rect rect) => RRect.fromRectAndRadius(rect, Radius.circular(radius));

  @override
  Path getInnerPath(Rect rect, {TextDirection? textDirection}) =>
      Path()..addRRect(_shape(rect).deflate(borderSide.width));

  @override
  Path getOuterPath(Rect rect, {TextDirection? textDirection}) => Path()..addRRect(_shape(rect));

  @override
  void paint(Canvas canvas, Rect rect,
      {double? gapStart, double gapExtent = 0.0, double gapPercentage = 0.0, TextDirection? textDirection}) {
    if (borderSide.style == BorderStyle.none || borderSide.width == 0) return;
    canvas.drawRRect(_shape(rect).deflate(borderSide.width / 2), borderSide.toPaint());
  }

  @override
  ShapeBorder? lerpFrom(ShapeBorder? a, double t) {
    if (a is YhFieldBorder) {
      return YhFieldBorder(
          borderSide: BorderSide.lerp(a.borderSide, borderSide, t), radius: lerpDouble(a.radius, radius, t)!);
    }
    return super.lerpFrom(a, t);
  }

  @override
  ShapeBorder? lerpTo(ShapeBorder? b, double t) {
    if (b is YhFieldBorder) {
      return YhFieldBorder(
          borderSide: BorderSide.lerp(borderSide, b.borderSide, t), radius: lerpDouble(radius, b.radius, t)!);
    }
    return super.lerpTo(b, t);
  }

  @override
  bool operator ==(Object other) =>
      other is YhFieldBorder && other.borderSide == borderSide && other.radius == radius;

  @override
  int get hashCode => Object.hash(borderSide, radius);
}

/// 页面转场：280 ms ease-out，轻微上移 + 淡入。
class _FadeSlideTransitions extends PageTransitionsBuilder {
  const _FadeSlideTransitions();

  @override
  Widget buildTransitions<T>(PageRoute<T> route, BuildContext context, Animation<double> animation,
      Animation<double> secondaryAnimation, Widget child) {
    final curved = CurvedAnimation(parent: animation, curve: Curves.easeOutCubic);
    return FadeTransition(
      opacity: curved,
      child: SlideTransition(
        position: Tween(begin: const Offset(0, 0.025), end: Offset.zero).animate(curved),
        child: child,
      ),
    );
  }
}
