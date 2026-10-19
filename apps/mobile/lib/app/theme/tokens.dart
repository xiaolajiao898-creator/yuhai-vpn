import 'package:flutter/material.dart';

/// 设计令牌（第 21 轮「宇海统一视觉」：官网 / App / 后台共用同一套色彩角色，
/// App 使用其深色一侧）。所有页面只从这里取颜色、字号、圆角、间距。
/// 旧名称（abyss/current/reef/moonlight/haze/glow/starlight/coral）保留为角色别名，
/// 数值已换成统一色板，避免逐页改名引入回归。
abstract final class YhColors {
  // 品牌：青柠 #BBEF68，墨色 #14201C。中性色是偏海军的深色，不再使用旧的青色。
  static const bg = Color(0xFF0C1218); // 页面背景
  static const paper = Color(0xFF141C24); // 卡片、面板、弹层
  static const soft = Color(0xFF1C2832); // 次级表面、输入框、选中底
  static const line = Color(0xFF2C3C48); // 分隔线、描边
  static const ink = Color(0xFFF3F6F1); // 主文字
  static const sub = Color(0xFFA8B4AE); // 次要文字
  static const accent = Color(0xFFBBEF68); // 品牌强调
  static const button = Color(0xFFBBEF68); // 主按钮底
  static const buttonInk = Color(0xFF14201C); // 主按钮文字
  static const good = Color(0xFFBBEF68); // 已连接
  static const warn = Color(0xFFF0C36A); // 提醒、即将到期、待处理
  static const danger = Color(0xFFF3A3AD); // 错误、失败、高风险操作

  /// 浅色令牌。营销站使用；App 当前以深色为产品界面。
  static const lightBg = Color(0xFFF4F6F1);
  static const lightPaper = Color(0xFFFFFFFF);
  static const lightInk = Color(0xFF14201C);
  static const lightSub = Color(0xFF3E4C46);

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
  static const surface = 22.0; // 卡片、连接面板
  static const sheet = 28.0; // 底部面板
  static const button = 16.0; // 主按钮
  static const listSelected = 12.0; // 列表选中态
  static const tag = 8.0; // 标签
}

abstract final class YhSpace {
  static const unit = 8.0; // 8 pt 网格
  static const page = 24.0; // 页面左右边距
  static const panel = 20.0; // 信息面板内边距
}

abstract final class YhFont {
  /// 界面中文与数字。已打进安装包的 Noto Sans SC。
  static const ui = 'NotoSansSC';
  static const numeric = ui;
  static const brand = ui;
  static const sizes = [40.0, 28.0, 20.0, 16.0, 14.0, 12.0];
  static const lineHeight = 1.5;
}

abstract final class YhMotion {
  static const pageTransition = Duration(milliseconds: 260);
  static const pageOffset = 16.0;
  static const rollingNumber = Duration(milliseconds: 180);
}
