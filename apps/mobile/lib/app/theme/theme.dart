import 'package:flutter/material.dart';

import 'tokens.dart';

ThemeData buildDeepSeaTheme() {
  const scheme = ColorScheme.dark(
    surface: YhColors.bg,
    onSurface: YhColors.ink,
    surfaceContainerHighest: YhColors.soft,
    primary: YhColors.accent,
    onPrimary: YhColors.buttonInk,
    secondary: YhColors.warn,
    error: YhColors.danger,
    outline: YhColors.line,
    outlineVariant: YhColors.line,
  );
  const body = TextStyle(
    color: YhColors.moonlight,
    height: YhFont.lineHeight,
    fontFamily: YhFont.ui,
    fontFamilyFallback: [
      YhFont.ui,
      'PingFang SC',
      'Microsoft YaHei',
      'Noto Sans CJK SC',
      'sans-serif',
    ],
  );
  final text = TextTheme(
    displayLarge: body.copyWith(fontSize: 40, fontWeight: FontWeight.w600),
    headlineMedium: body.copyWith(fontSize: 28, fontWeight: FontWeight.w600),
    titleLarge: body.copyWith(fontSize: 20, fontWeight: FontWeight.w500),
    bodyLarge: body.copyWith(fontSize: 16),
    bodyMedium: body.copyWith(fontSize: 14),
    labelSmall: body.copyWith(fontSize: 12, color: YhColors.haze),
  );
  return ThemeData(
    useMaterial3: true,
    fontFamily: YhFont.ui,
    colorScheme: scheme,
    scaffoldBackgroundColor: YhColors.abyss,
    textTheme: text,
    dividerColor: YhColors.reef,
    dividerTheme: const DividerThemeData(color: YhColors.reef, thickness: 1),
    splashFactory: NoSplash.splashFactory, // 无投影、无水波：层级只靠明度与 1px 描边
    bottomSheetTheme: const BottomSheetThemeData(
      backgroundColor: YhColors.current,
      shape: RoundedRectangleBorder(
        borderRadius:
            BorderRadius.vertical(top: Radius.circular(YhRadius.sheet)),
      ),
    ),
    appBarTheme: const AppBarTheme(
      backgroundColor: YhColors.bg,
      foregroundColor: YhColors.ink,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
      titleTextStyle: TextStyle(
          color: YhColors.ink,
          fontSize: 18,
          fontWeight: FontWeight.w600,
          fontFamily: YhFont.ui),
    ),
    navigationBarTheme: NavigationBarThemeData(
      backgroundColor: YhColors.paper,
      indicatorColor: YhColors.soft,
      surfaceTintColor: Colors.transparent,
      iconTheme: WidgetStateProperty.resolveWith((states) => IconThemeData(
          color: states.contains(WidgetState.selected)
              ? YhColors.accent
              : YhColors.sub)),
      labelTextStyle: WidgetStateProperty.resolveWith((states) => TextStyle(
          fontSize: 12,
          fontFamily: YhFont.ui,
          color: states.contains(WidgetState.selected)
              ? YhColors.accent
              : YhColors.sub)),
    ),
    cardTheme: CardThemeData(
      color: YhColors.paper,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
          side: const BorderSide(color: YhColors.line),
          borderRadius: BorderRadius.circular(YhRadius.surface)),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        backgroundColor: YhColors.button,
        foregroundColor: YhColors.buttonInk,
        minimumSize: const Size.fromHeight(56),
        shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(YhRadius.button)),
        textStyle: const TextStyle(
            fontSize: 16, fontWeight: FontWeight.w600, fontFamily: YhFont.ui),
      ),
    ),
    // 填充圆角框不能用 OutlineInputBorder。描边框把浮动标签钉在上边线中央，
    // BorderSide.none 时字就压在填充块的顶边上。非描边边框让标签浮在框内。
    inputDecorationTheme: const InputDecorationTheme(
      filled: true,
      fillColor: YhColors.soft,
      floatingLabelBehavior: FloatingLabelBehavior.auto,
      contentPadding: EdgeInsets.fromLTRB(16, 18, 16, 14),
      border: _filledFieldBorder,
      enabledBorder: _filledFieldBorder,
      focusedBorder: _filledFieldBorder,
      disabledBorder: _filledFieldBorder,
      errorBorder: _filledFieldBorder,
      focusedErrorBorder: _filledFieldBorder,
    ),
    pageTransitionsTheme: const PageTransitionsTheme(builders: {
      TargetPlatform.android: _FadeSlideTransitions(),
      TargetPlatform.iOS: _FadeSlideTransitions(),
    }),
  );
}

/// 四角圆角、不画线。填充色来自主题，标签留在框内。
const _filledFieldBorder = UnderlineInputBorder(
  borderSide: BorderSide.none,
  borderRadius: BorderRadius.all(Radius.circular(YhRadius.listSelected)),
);

/// 页面转场：260 ms ease-out，16 pt 位移 + 淡入（方案 9.2 动效）。
class _FadeSlideTransitions extends PageTransitionsBuilder {
  const _FadeSlideTransitions();

  @override
  Widget buildTransitions<T>(
      PageRoute<T> route,
      BuildContext context,
      Animation<double> animation,
      Animation<double> secondaryAnimation,
      Widget child) {
    final curved = CurvedAnimation(parent: animation, curve: Curves.easeOut);
    return FadeTransition(
      opacity: curved,
      child: SlideTransition(
        position: Tween(begin: const Offset(0, 0.02), end: Offset.zero)
            .animate(curved),
        child: child,
      ),
    );
  }
}
