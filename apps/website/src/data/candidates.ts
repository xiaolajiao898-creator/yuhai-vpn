// 当前对外下载地址。SHA-256 不从旧的 1.1.0 安装包借用。
// 正式发布时由构建脚本写入哈希；在那之前页面只显示占位说明。
export const sha256Placeholder = '正式发布时填入';

export const candidates = {
  android: {
    label: '安卓',
    href: '/download/yuhai-vpn-candidate-0928.apk',
    file: 'yuhai-vpn-candidate-0928.apk',
    note: 'Android 8.0 及以上。校验值随正式版本公布，不要拿旧版的数字来对。',
  },
  windows: {
    label: 'Windows',
    href: '/download/YuhaiVPN-Windows-x64-candidate.exe',
    file: 'YuhaiVPN-Windows-x64-candidate.exe',
    note: '64 位 Windows。这一版还没有签名，系统可能提示未知发布者。',
  },
};
