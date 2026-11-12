import 'package:flutter/material.dart';

/// 设计令牌（2026-11 改版：官网和 App 用同一套）。所有页面只从这里取颜色、字号、圆角、间距。
///
/// 第二版「米白」：暖米白底 + 品牌青柠 #BBEF68（只用来填色）+ 深绿字，三道波浪做主图形，
/// 配色取自吉祥物艾丽的制服（青柠上衣、藏青领口）。大标题用得意黑（YhDisplay），
/// 价格、计时这类大号数字和拉丁字母用 Unbounded（YhLatin），正文仍是 Noto Sans SC。
/// 旧名称（abyss/current/reef/moonlight/haze/glow/starlight/coral）保留为角色别名，
/// 数值跟着新色板走，避免逐页改名引入回归。
abstract final class YhColors {
  // 底和面
  static const bg = Color(0xFFF5F0E6); // 页面背景：暖米白
  static const deep = Color(0xFFFFFDF8); // 输入框底（比页面亮一级，像纸面上的白卡）
  static const paper = Color(0xFFFFFDF8); // 卡片、面板、弹层
  static const soft = Color(0xFFEEE8DB); // 次级表面、选中底、图标底
  static const line = Color(0xFFE2DACB); // 分隔线、描边

  // 文字
  static const ink = Color(0xFF14201C); // 主文字：品牌墨绿
  static const sub = Color(0xFF5E6A62); // 次要文字
  static const faint = Color(0xFF8C958E); // 更弱的说明文字

  // 品牌色
  static const lime = Color(0xFFBBEF68); // 品牌青柠：只用来填色（按钮、图标底、选中）
  static const limeDeep = Color(0xFF8DBF3F); // 青柠的暗面：立体圆环的阴影、潮汐线
  static const accent = lime; // 旧名：填色用的品牌色
  static const accentInk = Color(0xFF3F6B10); // 米白底上的绿色文字、图标、细线（对比度够）
  static const navy = Color(0xFF12293A); // 艾丽制服的藏青：深色按钮、提示条
  static const button = lime; // 主按钮底
  static const buttonInk = Color(0xFF14201C); // 主按钮文字、品牌墨色

  // 状态
  static const good = Color(0xFF3F6B10); // 已连接、已完成（文字）
  static const flow = Color(0xFF2A8C78); // 连接中
  static const warn = Color(0xFFA66A00); // 提醒、即将到期、待处理
  static const danger = Color(0xFFC2453A); // 错误、失败、高风险操作

  /// 半透明描边：叠在米白或白卡上都是一条细线。
  static const hair = Color(0x1A14201C);
  static const hairStrong = Color(0x2E14201C);
  static const hairLime = Color(0x668DBF3F);

  /// 浅色令牌：青柠卡片上的文字。
  static const lightBg = Color(0xFFF5F0E6);
  static const lightPaper = Color(0xFFFFFDF8);
  static const lightInk = Color(0xFF14201C);
  static const lightSub = Color(0xFF3E4B43);

  // —— 旧名称（角色别名）——
  static const abyss = bg;
  static const current = paper;
  static const reef = line;
  static const moonlight = ink;
  static const haze = sub;
  static const glow = good;
  static const starlight = warn;
  static const coral = danger;
}

abstract final class YhRadius {
  static const surface = 24.0; // 卡片、连接面板
  static const sheet = 32.0; // 底部面板、对话框
  static const button = 28.0; // 主按钮（56 高时就是胶囊）
  static const field = 18.0; // 输入框
  static const listSelected = 14.0; // 列表选中态、小图标底
  static const tag = 999.0; // 标签（胶囊）
}

abstract final class YhSpace {
  static const unit = 8.0; // 8 pt 网格
  static const page = 24.0; // 页面左右边距
  static const panel = 20.0; // 信息面板内边距
}

abstract final class YhFont {
  /// 界面中文与正文。已打进安装包的 Noto Sans SC。
  static const ui = 'NotoSansSC';

  /// 地址、订单号、表格里的数字：等宽数字的正文字体，长串也好认。
  static const numeric = ui;

  /// 大标题：得意黑（Smiley Sans，OFL），斜体压缩的展示字体。缺字时回落到 Noto。
  static const display = 'YhDisplay';

  /// 大号数字与拉丁字母：Unbounded（OFL）。价格、计时、编号、英文小标签。
  static const latin = 'YhLatin';

  static const brand = display;
  static const sizes = [40.0, 28.0, 20.0, 16.0, 14.0, 12.0];
  static const lineHeight = 1.5;
  static const fallback = [ui, 'PingFang SC', 'Microsoft YaHei', 'Noto Sans CJK SC', 'sans-serif'];
}

/// 常用的展示字样式。颜色默认墨绿，按需 copyWith。
abstract final class YhType {
  static TextStyle display(double size, {Color color = YhColors.ink, double height = 1.12}) => TextStyle(
        fontFamily: YhFont.display,
        fontFamilyFallback: YhFont.fallback,
        fontSize: size,
        height: height,
        fontWeight: FontWeight.w400,
        color: color,
        letterSpacing: 0,
      );

  static TextStyle latin(double size,
          {Color color = YhColors.ink, FontWeight weight = FontWeight.w700, double spacing = 0, double height = 1.1}) =>
      TextStyle(
        fontFamily: YhFont.latin,
        fontFamilyFallback: YhFont.fallback,
        fontSize: size,
        height: height,
        fontWeight: weight,
        color: color,
        letterSpacing: spacing,
        fontFeatures: const [FontFeature.tabularFigures()],
      );

  /// 小号英文标签（EYEBROW）：字距拉开、全大写。
  static TextStyle eyebrow({Color color = YhColors.accentInk}) =>
      latin(10.5, color: color, weight: FontWeight.w500, spacing: 2.2, height: 1.2);
}

abstract final class YhMotion {
  static const pageTransition = Duration(milliseconds: 280);
  static const pageOffset = 16.0;
  static const rollingNumber = Duration(milliseconds: 180);
}
