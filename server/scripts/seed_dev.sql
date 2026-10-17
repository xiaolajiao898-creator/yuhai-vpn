-- 开发环境种子数据：套餐、节点组与节点。收款地址请用 `go run ./cmd/fundsign` 生成带签名的 INSERT 后执行。
INSERT INTO plans (code, name, plan_type, tier, duration_days, period_quota_bytes, device_limit, price_micro, display_config, sort_order, status) VALUES
  ('STD_1M',  '标准版 1 个月',  'TIME', 1, 30,  200 * 1024::bigint * 1024 * 1024, 3, 9900000,   '{"badge": null}',      10, 'ON_SALE'),
  ('STD_12M', '标准版 12 个月', 'TIME', 1, 365, 200 * 1024::bigint * 1024 * 1024, 3, 99000000,  '{"badge": "省 17%"}', 11, 'ON_SALE'),
  ('PRO_1M',  '高级版 1 个月',  'TIME', 2, 30,  NULL, 5, 19900000,  '{"badge": "不限量"}', 20, 'ON_SALE'),
  ('PRO_12M', '高级版 12 个月', 'TIME', 2, 365, NULL, 5, 179900000, '{"badge": "推荐"}',   21, 'ON_SALE')
ON CONFLICT (code) DO NOTHING;
INSERT INTO plans (code, name, plan_type, tier, pack_bytes, validity_days, device_limit, price_micro, sort_order, status) VALUES
  ('PACK_100G', '100 GB 流量包（90 天）', 'TRAFFIC', 1, 100 * 1024::bigint * 1024 * 1024, 90, 3, 12900000, 30, 'ON_SALE')
ON CONFLICT (code) DO NOTHING;
INSERT INTO plans (code, name, plan_type, tier, extra_devices, price_micro, sort_order, status) VALUES
  ('DEV_ADDON_1', '加购 1 台设备', 'DEVICE_ADDON', 1, 1, 2000000, 40, 'ON_SALE')
ON CONFLICT (code) DO NOTHING;

INSERT INTO node_groups (name, min_tier, remark) VALUES ('标准', 1, NULL), ('高级', 2, '高级版专享') ON CONFLICT (name) DO NOTHING;
INSERT INTO nodes (group_id, name, country_code, city, region, host, status, is_recommended, load_score, sort_order)
SELECT g.id, v.name, v.cc, v.city, v.region, v.host, 'ONLINE', v.rec, v.load, v.sort
  FROM (VALUES
    ('标准', '东京 01',   'JP', 'Tokyo',     'ASIA',     'jp01.mock.yuhai.local', true,  35, 1),
    ('标准', '新加坡 01', 'SG', 'Singapore', 'ASIA',     'sg01.mock.yuhai.local', false, 52, 2),
    ('标准', '洛杉矶 01', 'US', 'Los Angeles','AMERICAS','us01.mock.yuhai.local', false, 41, 3),
    ('高级', '东京 02',   'JP', 'Tokyo',     'ASIA',     'jp02.mock.yuhai.local', true,  18, 4),
    ('高级', '法兰克福 02','DE','Frankfurt', 'EUROPE',   'de02.mock.yuhai.local', false, 22, 5)
  ) AS v(grp, name, cc, city, region, host, rec, load, sort)
  JOIN node_groups g ON g.name = v.grp
ON CONFLICT DO NOTHING;

INSERT INTO system_configs (key, value) VALUES
  ('order.cooldown_seconds', '7200'), ('topup.min_micro', '1000000'), ('topup.max_micro', '1000000000')
ON CONFLICT (key) DO NOTHING;
