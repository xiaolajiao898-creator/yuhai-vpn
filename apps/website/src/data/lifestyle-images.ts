// 官网生活方式横幅图清单（唯一真源）。只有「授权或生成记录完整」的条目才会被页面当作图片使用；
// 其余一律显示抽象背景的静态回退，绝不临时抓取来源不明的图片。
// 人物图只用于营造「安心、私密、自由使用」的氛围：不是真实用户，不是评价，也不是任何安全证据。
export type LifestyleImage = {
  id: string;
  /** 画面要求（交给授权图库检索或图像生成时使用） */
  brief: string;
  /** 替代文本：描述画面本身，不暗示是真实客户 */
  alt: string;
  /** 裁剪：桌面 16:9，手机 4:5；focal 为主体所在位置（CSS object-position） */
  crop: { desktop: '16:9'; mobile: '4:5'; focal: string };
  /** 授权 / 生成记录。status 不是 licensed 或 generated、或记录不完整时，页面只用静态回退。 */
  rights: {
    status: 'pending' | 'licensed' | 'generated';
    source: string; // 图库名与图片编号，或生成工具与版本
    license: string; // 授权类型与范围，或生成工具的使用条款
    record: string; // 购买凭据编号 / 生成提示词与种子的存档路径
    model_release?: string; // 图库人像授权（model release）编号
  };
  /** 图片文件（放在 public/assets/lifestyle/）；为空表示尚无文件 */
  files?: { desktopAvif: string; desktopWebp: string; mobileAvif: string; mobileWebp: string; fallbackJpg: string; width: number; height: number };
};

const shared =
  '成年女性（外观约 25–40 岁、金发、欧美面孔），自然光、明亮、生活化，不摆拍、不性感化，穿着日常；' +
  '无可识别的真实个人身份、无名人、无任何品牌 Logo 或可辨认商标（包括笔记本/手机背面标志需遮挡或虚化）；' +
  '屏幕内容虚化或朝向画外，不出现伪造的 App 界面；构图在一侧留出大面积干净留白以放置文字。';

export const lifestyleImages: LifestyleImage[] = [
  {
    id: 'act-encrypt',
    brief: `咖啡馆靠窗座位，她在笔记本电脑上安静工作并微笑，桌上一杯咖啡。${shared} 主体偏右，左侧留白。`,
    alt: '一位女士在明亮的咖啡馆里用笔记本电脑工作（示意图片，非真实用户）',
    crop: { desktop: '16:9', mobile: '4:5', focal: '70% 40%' },
    rights: { status: 'pending', source: '', license: '', record: '' },
  },
  {
    id: 'act-regions',
    brief: `旅行途中的临时办公：酒店阳台或机场休息区，她低头看手机，身旁有行李箱，表情轻松。${shared} 主体偏左，右侧留白。`,
    alt: '一位女士在旅途中的阳台上看手机（示意图片，非真实用户）',
    crop: { desktop: '16:9', mobile: '4:5', focal: '30% 45%' },
    rights: { status: 'pending', source: '', license: '', record: '' },
  },
  {
    id: 'act-privacy',
    brief: `居家客厅，沙发上盘腿而坐，用手机或平板，窗外自然光，氛围放松、私密。${shared} 主体偏右，左侧留白。`,
    alt: '一位女士在明亮的客厅沙发上使用手机（示意图片，非真实用户）',
    crop: { desktop: '16:9', mobile: '4:5', focal: '65% 50%' },
    rights: { status: 'pending', source: '', license: '', record: '' },
  },
];

/** 记录完整、文件齐全才可使用；否则返回 undefined，调用方使用静态回退。 */
export function usableImage(id: string): LifestyleImage | undefined {
  const img = lifestyleImages.find((i) => i.id === id);
  if (!img || !img.files) return undefined;
  const r = img.rights;
  const ok =
    (r.status === 'licensed' && !!r.source && !!r.license && !!r.record && !!r.model_release) ||
    (r.status === 'generated' && !!r.source && !!r.license && !!r.record);
  return ok ? img : undefined;
}
