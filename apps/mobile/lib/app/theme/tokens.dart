import 'package:flutter/material.dart';

/// 设计令牌（第 21 轮「宇海统一视觉」：官网 / App / 后台共用同一套色彩角色，
/// App 使用其深色一侧）。所有页面只从这里取颜色、字号、圆角、间距。
/// 旧名称（abyss/current/reef/moonlight/haze/glow/starlight/coral）保留为角色别名，
/// 数值已换成统一色板，避免逐页改名引入回归。
abstract final class YhColors {
  // —— 统一角色（与 apps/website、apps/admin-web 的 --yh-* 变量一一对应）——
  static const bg = Color(0xFF08121F); // 页面背景
  static const paper = Color(0xFF111F2E); // 卡片、面板、弹层
  static const soft = Color(0xFF172A3D); // 次级表面、输入框、选中底
  static const line = Color(0xFF2A4054); // 分隔线、描边
  static const ink = Color(0xFFEDF6FC); // 主文字
  static const sub = Color(0xFFA1B4C5); // 次要文字
  static const accent = Color(0xFF79D0E3); // 品牌强调、可操作元素
  static const button = Color(0xFFA9DCE9); // 主按钮底
  static const buttonInk = Color(0xFF0A273A); // 主按钮文字
  static const good = Color(0xFF75D8B7); // 仅「已连接 / 已生效」
  static const warn = Color(0xFFF2B46E); // 提醒、即将到期、待处理
  static const danger = Color(0xFFF2A3AE); // 错误、失败、高风险操作

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
  static const numeric = 'IBMPlexSans'; // 数字与英文（等宽数字）
  static const brand = 'NotoSerifSC'; // 仅引导页主标题
  static const sizes = [40.0, 28.0, 20.0, 16.0, 14.0, 12.0];
  static const lineHeight = 1.5;
}

abstract final class YhMotion {
  static const pageTransition = Duration(milliseconds: 260);
  static const pageOffset = 16.0;
  static const rollingNumber = Duration(milliseconds: 180);
}
