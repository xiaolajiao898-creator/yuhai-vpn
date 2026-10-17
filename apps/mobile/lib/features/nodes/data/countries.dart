// 国家/地区显示名与搜索。只做展示：线路的真实位置以服务端登记为准，
// 这里不会因为国家代码就声称某条线路是「实体专线」。

/// 用户最关心、在节点页置顶展示的地区（没有可用线路时不显示，不会显示成可连接）。
const featuredCountries = ['SG', 'US', 'JP'];

const _names = {
  'SG': '新加坡',
  'US': '美国',
  'JP': '日本',
  'HK': '中国香港',
  'TW': '中国台湾',
  'MO': '中国澳门',
  'KR': '韩国',
  'MY': '马来西亚',
  'TH': '泰国',
  'VN': '越南',
  'PH': '菲律宾',
  'ID': '印度尼西亚',
  'IN': '印度',
  'AU': '澳大利亚',
  'NZ': '新西兰',
  'GB': '英国',
  'DE': '德国',
  'FR': '法国',
  'NL': '荷兰',
  'IT': '意大利',
  'ES': '西班牙',
  'SE': '瑞典',
  'CH': '瑞士',
  'PL': '波兰',
  'TR': '土耳其',
  'RU': '俄罗斯',
  'AE': '阿联酋',
  'CA': '加拿大',
  'MX': '墨西哥',
  'BR': '巴西',
  'AR': '阿根廷',
  'ZA': '南非',
};

const _english = {
  'SG': 'singapore',
  'US': 'united states usa america',
  'JP': 'japan',
  'HK': 'hong kong',
  'TW': 'taiwan',
  'KR': 'korea',
  'MY': 'malaysia',
  'GB': 'united kingdom uk britain',
  'DE': 'germany',
  'AU': 'australia',
  'CA': 'canada',
};

String countryName(String code) =>
    code.isEmpty ? '未标注地区' : (_names[code] ?? code);

/// 国旗 emoji：由两个区域指示符组成；代码不合法时返回空串（界面改用首字母）。
String countryFlag(String code) {
  if (!RegExp(r'^[A-Z]{2}$').hasMatch(code)) return '';
  return String.fromCharCodes(code.codeUnits.map((c) => 0x1F1E6 + c - 0x41));
}

/// 搜索：国家中文名、英文名、国家代码、城市、线路名称，忽略大小写与首尾空白。
bool matchesQuery(
    {required String query,
    required String countryCode,
    required String city,
    required String name}) {
  final q = query.trim().toLowerCase();
  if (q.isEmpty) return true;
  final haystack = [
    countryName(countryCode),
    _english[countryCode] ?? '',
    countryCode,
    city,
    name,
  ].join(' ').toLowerCase();
  return q.split(RegExp(r'\s+')).every(haystack.contains);
}
