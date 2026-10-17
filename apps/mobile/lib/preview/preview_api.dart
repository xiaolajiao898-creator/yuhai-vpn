import 'dart:typed_data';
import 'package:dio/dio.dart';
import '../core/network/api_client.dart';
import '../core/security/device_key.dart';
import '../core/storage/session_store.dart';
import '../features/settings/data/connection_preferences.dart';

class PreviewPreferencesStorage implements ConnectionPreferencesStorage {
  ConnectionPreferences _value = const ConnectionPreferences();
  @override
  Future<ConnectionPreferences> load() async => _value;
  @override
  Future<void> save(ConnectionPreferences preferences) async {
    _value = preferences;
  }
}

/// Only imported by preview_main.dart. No account secrets or real API requests.
class PreviewSessionStore extends SessionStore {
  Session? _session = const Session(
      accountNo: 'YH888876',
      deviceId: '1',
      accessToken: 'preview-only',
      refreshToken: 'preview-only',
      inviteCode: 'YH8888');
  String _uid = 'preview-device';
  @override
  Future<String?> deviceUid() async => _uid;
  @override
  Future<void> saveDeviceUid(String uid) async {
    _uid = uid;
  }

  @override
  Future<Session?> session() async => _session;
  @override
  Future<void> saveSession(Session s) async {
    _session = s;
  }

  @override
  Future<void> clearSession() async {
    _session = null;
  }
}

class PreviewDeviceKey implements DeviceKey {
  @override
  Future<String> publicKeySpkiBase64() async =>
      throw ApiException(40001, '请点击顶部重置演示，返回示例账户');
  @override
  Future<Uint8List> signDigest(Uint8List digest) async =>
      throw ApiException(40001, '演示不签署真实请求');
  @override
  Future<void> regenerate() async {}
}

class PreviewApi extends ApiClient {
  PreviewApi(PreviewSessionStore session)
      : super(
            baseUrl: 'https://preview.invalid',
            deviceKey: PreviewDeviceKey(),
            store: session,
            appVersion: 'preview',
            platform: 'WEB') {
    dio.interceptors.add(InterceptorsWrapper(
        onRequest: (options, handler) => handler
            .reject(DioException(requestOptions: options, error: '预览禁止网络请求'))));
  }
  final DateTime now = DateTime.now().toUtc();
  final Set<String> _removed = {};
  String? _boundInvite;
  String? _bindIdempotencyKey;
  String? _bindPayload;
  String at(int days) => now.add(Duration(days: days)).toIso8601String();
  Map<String, dynamic> get _me => {
        'account_no': 'YH888876',
        'status': 'ACTIVE',
        'backup_acknowledged': true,
        'balance': '1200.000000',
        'pending_order': null,
        'security_notices': [],
        'announcements_unread': 0,
        'server_time': at(0),
        'entitlement': {
          'can_connect': true,
          'blocked_reason': null,
          'tier': 2,
          'plan_name': '高级版',
          'expire_at': at(28),
          'device_count': 2 - _removed.length,
          'device_limit': 3,
          'period_quota_bytes': 214748364800,
          'period_used_bytes': 34359738368,
          'period_reset_at': at(28),
          'traffic_packs': []
        },
      };
  List<Map<String, dynamic>> get _plans => [
        {
          'code': 'MONTH',
          'name': '高级版 · 月付',
          'type': 'TIME',
          'tier': 2,
          'list_price': '9.90',
          'duration_days': 30,
          'quota_bytes': 214748364800,
          'device_limit': 3,
          'badge': '灵活之选'
        },
        {
          'code': 'YEAR',
          'name': '高级版 · 年付',
          'type': 'TIME',
          'tier': 2,
          'list_price': '89.00',
          'duration_days': 365,
          'quota_bytes': 214748364800,
          'device_limit': 3,
          'badge': '推荐'
        },
        {
          'code': 'TRAFFIC',
          'name': '100 GB 流量包',
          'type': 'TRAFFIC',
          'tier': 0,
          'list_price': '3.00',
          'quota_bytes': 107374182400
        },
        {
          'code': 'DEVICE',
          'name': '额外设备',
          'type': 'DEVICE_ADDON',
          'tier': 0,
          'list_price': '2.00',
          'device_limit': 1
        },
      ];
  Map<String, dynamic> _page(
      List<Map<String, dynamic>> items, Map<String, List<String>> query) {
    final start = int.tryParse(query['cursor']?.firstOrNull ?? '') ?? 0;
    final end = (start + 20).clamp(0, items.length);
    return {
      'items': items.sublist(start.clamp(0, items.length), end),
      'next_cursor': end < items.length ? '$end' : null
    };
  }

  @override
  Future<Map<String, dynamic>> call(String method, String path,
      {Map<String, dynamic>? json,
      Map<String, List<String>> query = const {},
      bool sign = false,
      String? idempotencyKey,
      bool retrying = false, Session? expectedSession}) async {
    if (await store.session() == null) {
      throw ApiException(40101, '请重置演示以返回示例账户');
    }
    if (method == 'GET' && path == '/v1/me') return _me;
    if (method == 'GET' && path == '/v1/traffic/daily') {
      final count = int.tryParse(query['days']?.firstOrNull ?? '') ?? 30;
      if (count < 1 || count > 90) throw ApiException(40001, '天数应为1到90');
      return {
        'days': [for (var i = 0; i < count; i++) _trafficDay(count - 1 - i)]
      };
    }
    if (method == 'GET' && path == '/v1/devices') {
      return {
        'device_limit': 3,
        'devices': [
          {
            'id': '1',
            'display_name': '我的手机',
            'platform': 'IOS',
            'model': 'iPhone',
            'is_current': true,
            'last_seen_at': at(0),
            'created_at': at(-30)
          },
          if (!_removed.contains('2'))
            {
              'id': '2',
              'display_name': '备用手机',
              'platform': 'ANDROID',
              'model': 'Android',
              'is_current': false,
              'last_seen_at': at(-1),
              'created_at': at(-12)
            },
        ]
      };
    }
    if (method == 'DELETE' && path == '/v1/devices/2') {
      _removed.add('2');
      return {};
    }
    if (method == 'POST' && path == '/v1/auth/logout') return {};
    if (method == 'GET' && path == '/v1/nodes') {
      return {
        'nodes': [
          // 演示数据：地区与城市只用于展示界面结构，不代表已开通的真实线路。
          for (final n in [
            ('sg1', '新加坡 · 01', 'SG', '新加坡', 'ASIA', 36),
            ('sg2', '新加坡 · 02', 'SG', '新加坡', 'ASIA', 51),
            ('us1', '洛杉矶 · 01', 'US', '洛杉矶', 'AMERICAS', 18),
            ('us2', '圣何塞 · 01', 'US', '圣何塞', 'AMERICAS', 27),
            ('us3', '纽约 · 01', 'US', '纽约', 'AMERICAS', 44),
            ('jp1', '东京 · 01', 'JP', '东京', 'ASIA', 42),
            ('jp2', '大阪 · 01', 'JP', '大阪', 'ASIA', 33),
            ('hk', '香港 · 01', 'HK', '香港', 'ASIA', 22),
            ('kr', '首尔 · 01', 'KR', '首尔', 'ASIA', 29),
            ('de', '法兰克福 · 01', 'DE', '法兰克福', 'EUROPE', 31)
          ])
            {
              'id': n.$1,
              'name': n.$2,
              'country_code': n.$3,
              'city': n.$4,
              'region': n.$5,
              'available': n.$1 != 'us3' && n.$1 != 'de',
              'locked_reason': n.$1 == 'us3'
                  ? 'OFFLINE'
                  : (n.$1 == 'de' ? 'NOT_READY' : null),
              'load': n.$1 == 'us3' || n.$1 == 'de' ? null : n.$6,
              'is_recommended': n.$1 == 'sg1',
              'probe': n.$1 == 'us3' || n.$1 == 'de'
                  ? null
                  : {'host': 'demo-${n.$1}.invalid', 'port': 443},
              'protocols':
                  n.$1 == 'us3' || n.$1 == 'de' ? <String>[] : ['MOCK']
            },
        ]
      };
    }
    if (method == 'GET' && path == '/v1/plans') return {'plans': _plans};
    if (method == 'POST' && path == '/v1/plans/quote') {
      final p = _plans.firstWhere((p) => p['code'] == json?['plan_code']);
      return {
        'quote_id': 'preview-quote',
        'plan': {'name': p['name']},
        'change_type': 'RENEW',
        'base_amount': p['list_price'],
        'lines': [
          {'label': '示例套餐价格', 'amount': p['list_price']}
        ],
        'balance_available': '1200.000000',
        'new_expire_at': at(58),
        'expires_at': now.add(const Duration(minutes: 15)).toIso8601String()
      };
    }
    if (method == 'GET' && path == '/v1/orders') {
      return _page([
        for (var i = 0; i < 4; i++)
          {
            'order_no': 'DEMO-202609-${100 + i}',
            'order_type': 'PLAN',
            'status': 'COMPLETED',
            'pay_method': 'BALANCE',
            'plan_name': '高级版 · 月付',
            'base_amount': '9.90',
            'pay_amount': '9.90',
            'fulfillment_result': 'PLAN_GRANTED',
            'expires_at': at(-i * 30),
            'completed_at': at(-i * 30),
            'created_at': at(-i * 30),
            'server_time': at(0)
          },
      ], query);
    }
    if (method == 'GET' && path == '/v1/invite') {
      return {
        'invite_code': 'YH8888',
        'invite_url': null,
        'binding_enabled': true,
        'rewards_enabled': false,
        'can_bind': _boundInvite == null,
        'bind_status': _boundInvite == null ? 'AVAILABLE' : 'BOUND',
        'bind_deadline': at(2),
        'stats': {
          'invited': 12,
          'paid': 8,
          'rewards_pending': 2,
          'rewards_released': 5
        },
        'rules': '奖励活动暂未开放。可使用 ABC234 体验邀请码绑定；当前人数与奖励列表均为示例，不代表真实收益。'
      };
    }
    if (method == 'POST' && path == '/v1/invite/bind') {
      final code = (json?['code'] as String? ?? '').trim().toUpperCase();
      final source = json?['method'];
      if (!RegExp(r'^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{6}$').hasMatch(code) ||
          (source != 'MANUAL' && source != 'CLIPBOARD') ||
          idempotencyKey == null ||
          idempotencyKey.isEmpty) {
        throw ApiException(40001, '请输入有效的六位邀请码');
      }
      final payload = '$code:$source';
      if (_bindIdempotencyKey == idempotencyKey) {
        if (_bindPayload != payload) throw ApiException(40901, '请求内容已改变');
      } else {
        if (_boundInvite != null) throw ApiException(40903, '已绑定邀请人');
        if (code == 'YH8888') throw ApiException(42201, '不能绑定自己的邀请码');
        _boundInvite = code;
        _bindIdempotencyKey = idempotencyKey;
        _bindPayload = payload;
      }
      return {
        'bound': true,
        'reward_description': '演示绑定成功。不会建立真实邀请关系或发放奖励，点击顶部重置演示可重新体验。'
      };
    }
    if (method == 'GET' && path == '/v1/invite/rewards') {
      return _page([
        for (var i = 0; i < 8; i++)
          {
            'id': '${i + 1}',
            'role': 'INVITER',
            'is_example': true,
            'kind': i == 2
                ? 'TRAFFIC'
                : i.isEven
                    ? 'DAYS'
                    : 'BALANCE',
            'days': i.isEven && i != 2 ? 7 : null,
            'bytes': i == 2 ? 10737418240 : null,
            'amount': i.isOdd ? '3.000000' : null,
            'status': i == 0
                ? 'PENDING'
                : i == 1
                    ? 'REVIEW'
                    : i == 7
                        ? 'REJECTED'
                        : 'RELEASED',
            'created_at': at(-i - 1),
            'release_at': i == 1 || i == 7
                ? null
                : i == 0
                    ? at(3)
                    : at(-i)
          },
      ], query);
    }
    if (method == 'GET' && path == '/v1/balance/ledger') {
      return _page([
        for (var i = 0; i < 24; i++)
          {
            'id': '${24 - i}',
            'kind': i == 0
                ? 'DEPOSIT'
                : i.isEven
                    ? 'INVITE_REWARD'
                    : 'PURCHASE',
            'amount': i == 0
                ? '1200.000000'
                : i.isEven
                    ? '3.000000'
                    : '-9.900000',
            'note': '示例记录 · 非真实收支',
            'created_at': at(-i)
          },
      ], query);
    }
    if (method == 'GET' && path == '/v1/entitlements/ledger') {
      return _page([
        {
          'id': '2',
          'kind': 'RENEW',
          'seconds_delta': 2592000,
          'note': '示例 · 月度套餐续费',
          'created_at': at(-2)
        },
        {
          'id': '1',
          'kind': 'TRAFFIC_PACK',
          'bytes_delta': 107374182400,
          'note': '示例 · 流量包',
          'created_at': at(-10)
        },
      ], query);
    }
    throw ApiException(40001, '此为界面演示，不创建真实账户、付款或订单');
  }

  @override
  Future<Map<String, dynamic>> selfSigned(
          String path, Map<String, dynamic> json,
          {String? idempotencyKey}) async =>
      throw ApiException(40001, '请点击顶部重置演示，返回示例账户');
  @override
  Future<void> refresh() async => throw ApiException(40001, '请重置演示');
  @override
  Future<Map<String, dynamic>> getPublic(String path,
          {Map<String, List<String>> query = const {}}) async =>
      throw ApiException(40401, '尚未发布正式版本');

  Map<String, dynamic> _trafficDay(int age) {
    final total = (640 + ((90 - age) * 173 % 2460)) * 1048576;
    final first = total * 7 ~/ 10;
    return {
      'date': at(-age).substring(0, 10),
      'up_bytes': total ~/ 5,
      'down_bytes': total - total ~/ 5,
      'by_device': [
        {'device_id': '1', 'bytes': first},
        {'device_id': age > 12 ? '3' : '2', 'bytes': total - first}
      ]
    };
  }

  @override
  Future<(Map<String, dynamic>?, String?)> getIfChanged(String path,
          {String? etag}) async =>
      (await call('GET', path), null);
}
